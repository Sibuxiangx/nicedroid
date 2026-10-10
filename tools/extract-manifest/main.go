// Command extract-manifest builds manifest.json from a Droid CLI binary.
//
// The Droid CLI keeps its model registry — ids, providers, reasoning efforts,
// cost multipliers — inside the JavaScript bundle compiled into the binary, and
// no API serves it. This tool scans the binary for those registry literals and
// writes them out as JSON, which the gateway embeds and reads at runtime.
//
// A registry entry looks like this (minified, keyed by model id):
//
//	"glm-5.3-flash":{id:"glm-5.3-flash",name:"GLM-5.3-Flash",provider:"factory",
//	  apiProviders:["fireworks","baseten","databricks"],
//	  apiProviderConfig:{fireworks:{...j,modelId:"accounts/fireworks/models/glm-5p3-flash"}},
//	  reasoningEffort:{supported:["low","high","max"],default:"high"},
//	  tier:"standard",billingPool:"core",images:!0,pdf:!1,
//	  cost:{tokenMultiplier:0.06,cacheReadTokenMultiplier:0.2,outputTokenMultiplier:3.34}}
//
// Field order differs between entries, so the parser anchors on the map key
// ("model-id":{id:"model-id",) and brace-matches the value instead of assuming an
// order. Values that are JS identifiers rather than literals (apiProviders:N,
// cost:{promotions:[Mt]}) are resolved when a single unambiguous definition
// exists and otherwise reported as missing rather than guessed at.
//
// This is best-effort parsing of a minified proprietary bundle. The tool
// validates its own output and exits non-zero when the extraction looks
// incomplete, so a change in the bundle layout fails CI instead of silently
// committing a truncated manifest.
//
// Usage:
//
//	go run ./tools/extract-manifest <droid-binary> --version <x.y.z> [--out manifest.json]
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const schemaVersion = 1

// ---------- manifest shape ----------

// Manifest is the generated model catalog.
type Manifest struct {
	Schema      int     `json:"schema"`
	Source      string  `json:"source"`
	GeneratedAt string  `json:"generated_at,omitempty"`
	CLI         CLIInfo `json:"cli"`
	Models      []Model `json:"models"`
}

// CLIInfo records which Droid build the manifest came from.
type CLIInfo struct {
	Version   string `json:"version"`
	UserAgent string `json:"user_agent"`
}

// Model is one entry of the Droid model registry, normalised for the gateway.
type Model struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Provider string `json:"provider"`
	// APIModelProvider is the CLI's own request-shape hint. Open-weight models
	// routed through Factory carry "generic-chat-completion-api"; native
	// provider models leave it empty and are identified by Provider.
	APIModelProvider string            `json:"api_model_provider,omitempty"`
	Providers        []string          `json:"providers"`
	ProviderModels   map[string]string `json:"provider_models,omitempty"`
	QuotaPool        string            `json:"quota_pool"`
	ServiceTier      string            `json:"service_tier,omitempty"`
	Vision           bool              `json:"vision"`
	PDF              bool              `json:"pdf"`
	Reasoning        Reasoning         `json:"reasoning"`
	Cost             Cost              `json:"cost"`
	Promotions       []Promotion       `json:"promotions,omitempty"`
	DeprecatedOn     string            `json:"deprecated_on,omitempty"`
	Replacement      string            `json:"replacement,omitempty"`
	CLIVisible       bool              `json:"cli_visible"`
}

// Reasoning lists the effort levels the model accepts.
type Reasoning struct {
	Efforts []string `json:"efforts"`
	Default string   `json:"default,omitempty"`
}

// Cost holds the credit-model inputs, mirroring the CLI's cost block.
type Cost struct {
	TokenMultiplier     float64 `json:"token_multiplier"`
	OutputMultiplier    float64 `json:"output_multiplier,omitempty"`
	CacheReadMultiplier float64 `json:"cache_read_multiplier,omitempty"`
}

// Promotion is a time-boxed price discount.
type Promotion struct {
	Discount  float64 `json:"discount,omitempty"`
	StartsAt  string  `json:"starts_at,omitempty"`
	ExpiresAt string  `json:"expires_at,omitempty"`
	Label     string  `json:"label,omitempty"`
}

// ---------- scanners ----------

var (
	// Registry map members are keyed by model id, written bare when the id is a
	// valid JS identifier (inkling:{...}) and quoted when it is not
	// ("glm-5.3-flash":{...}). The key is read by walking back from this marker.
	entryMarker = []byte(`:{id:"`)
	promoMarker = []byte(`={discount:`)

	reName        = regexp.MustCompile(`\bname:"((?:[^"\\]|\\.)*)"`)
	reProvider    = regexp.MustCompile(`\bprovider:"([^"]+)"`)
	reAPIModelPrv = regexp.MustCompile(`\bapiModelProvider:"([^"]+)"`)
	reTier        = regexp.MustCompile(`\btier:"([^"]+)"`)
	reBillingPool = regexp.MustCompile(`\bbillingPool:"([^"]+)"`)
	reEffortBlock = regexp.MustCompile(`reasoningEffort:\{([^}]*)\}`)
	reEffortSet   = regexp.MustCompile(`supported:\[([^\]]*)\]`)
	reEffortDef   = regexp.MustCompile(`\bdefault:"([^"]+)"`)
	reQuoted      = regexp.MustCompile(`"((?:[^"\\]|\\.)*)"`)
	reAPIConfig   = regexp.MustCompile(`([a-z_]+):\{[^{}]*?modelId:"([^"]+)"`)
	reAPIPrv      = regexp.MustCompile(`\bapiProviders:\[([^\]]*)\]`)
	reGlobalPrv   = regexp.MustCompile(`regionOverrides:\{global:\{apiProviders:\[([^\]]*)\]`)
	rePromos      = regexp.MustCompile(`promotions:\[([^\]]*)\]`)
	reTokenMult   = regexp.MustCompile(`\btokenMultiplier:(-?[0-9]+(?:\.[0-9]+)?)`)
	reOutputMult  = regexp.MustCompile(`\boutputTokenMultiplier:(-?[0-9]+(?:\.[0-9]+)?)`)
	reCacheMult   = regexp.MustCompile(`\bcacheReadTokenMultiplier:(-?[0-9]+(?:\.[0-9]+)?)`)
	reImages      = regexp.MustCompile(`\bimages:(!0|!1|true|false)`)
	rePDF         = regexp.MustCompile(`\bpdf:(!0|!1|true|false)`)
	// Deprecation is a block: deprecation:{date:"2026-09-25",hard:{fallbackModelId:"..."}}.
	// The date is a plain string, and fallbackModelId must be read from inside
	// this block rather than from anywhere else in the entry.
	reDeprecation = regexp.MustCompile(`deprecation:\{([^{}]*(?:\{[^{}]*\})?[^{}]*)\}`)
	reDepDate     = regexp.MustCompile(`\bdate:"([^"]+)"`)
	reFallback    = regexp.MustCompile(`\bfallbackModelId:"([^"]+)"`)
	reIdent       = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*$`)
	reModelID     = regexp.MustCompile(`^[a-z0-9][a-z0-9.\-]*$`)

	// Promotion definition bodies, found once and shared across models.
	rePromoDiscount = regexp.MustCompile(`\bdiscount:(-?[0-9]+(?:\.[0-9]+)?)`)
	rePromoStart    = regexp.MustCompile(`\bstartsAt:new Date\("([^"]+)"\)`)
	rePromoExpires  = regexp.MustCompile(`\bexpiresAt:new Date\("([^"]+)"\)`)
	rePromoLabel    = regexp.MustCompile(`\blabel:"((?:[^"\\]|\\.)*)"`)
)

// matchBalanced returns the balanced {...} or [...] block beginning at data[start].
func matchBalanced(data []byte, start int) []byte {
	opener := data[start]
	closer := byte('}')
	if opener == '[' {
		closer = ']'
	}
	depth := 0
	quote := byte(0)
	for i := start; i < len(data); i++ {
		c := data[i]
		switch {
		case quote != 0:
			if c == '\\' {
				i++
				continue
			}
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'' || c == '`':
			quote = c
		case c == opener:
			depth++
		case c == closer:
			depth--
			if depth == 0 {
				return data[start : i+1]
			}
		}
	}
	return nil
}

// findEntries returns {model id: object literal} for every registry entry.
func findEntries(data []byte) map[string][]byte {
	entries := make(map[string][]byte)
	for pos := 0; pos < len(data); {
		i := bytes.Index(data[pos:], entryMarker)
		if i < 0 {
			break
		}
		at := pos + i // index of the ':' before the object
		pos = at + len(entryMarker)

		// The map key sits just before that colon: quoted when the id is not a
		// valid JS identifier, bare when it is.
		if at == 0 {
			continue
		}
		var key string
		if data[at-1] == '"' {
			open := bytes.LastIndexByte(data[:at-1], '"')
			if open < 0 {
				continue
			}
			key = string(data[open+1 : at-1])
		} else {
			j := at
			for j > 0 && isIdentByte(data[j-1]) {
				j--
			}
			key = string(data[j:at])
		}
		if !reModelID.MatchString(key) {
			continue
		}
		// ...and must repeat as the object's own id field.
		idStart := at + len(entryMarker)
		idEnd := bytes.IndexByte(data[idStart:], '"')
		if idEnd < 0 || string(data[idStart:idStart+idEnd]) != key {
			continue
		}

		obj := matchBalanced(data, at+1) // at+1 is the '{'
		if obj == nil {
			continue
		}
		// Registry entries always carry a cost block; the id-keyed stub objects
		// elsewhere in the bundle ({id:"x",variants:[...]}) do not.
		if !bytes.Contains(obj, []byte("cost:{")) {
			continue
		}
		if _, seen := entries[key]; !seen {
			entries[key] = obj
		}
	}
	return entries
}

// findPromotionDefs collects NAME={discount:...} definitions in one pass.
func findPromotionDefs(data []byte) map[string][][]byte {
	defs := make(map[string][][]byte)
	for pos := 0; pos < len(data); {
		i := bytes.Index(data[pos:], promoMarker)
		if i < 0 {
			break
		}
		at := pos + i // index of '='
		pos = at + len(promoMarker)

		// Walk back over the identifier.
		j := at - 1
		for j >= 0 && (isIdentByte(data[j])) {
			j--
		}
		name := string(data[j+1 : at])
		if name == "" || !reIdent.MatchString(name) {
			continue
		}
		blk := matchBalanced(data, at+1)
		if blk == nil {
			continue
		}
		defs[name] = append(defs[name], blk)
	}
	return defs
}

func isIdentByte(c byte) bool {
	return c == '_' || c == '$' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// ---------- field readers ----------

func jstr(re *regexp.Regexp, obj string) string {
	if m := re.FindStringSubmatch(obj); m != nil {
		return m[1]
	}
	return ""
}

func jnum(re *regexp.Regexp, obj string) float64 {
	if m := re.FindStringSubmatch(obj); m != nil {
		v, _ := strconv.ParseFloat(m[1], 64)
		return v
	}
	return 0
}

func jbool(re *regexp.Regexp, obj string) bool {
	if m := re.FindStringSubmatch(obj); m != nil {
		return m[1] == "!0" || m[1] == "true"
	}
	return false
}

// quotedStrings returns the string literals in an array body, or nil when the
// body contains a spread and therefore cannot be read textually.
func quotedStrings(body string) []string {
	if strings.Contains(body, "...") {
		return nil
	}
	var out []string
	for _, m := range reQuoted.FindAllStringSubmatch(body, -1) {
		out = append(out, m[1])
	}
	return out
}

// transportProviders are registry entries that describe how the CLI reaches a
// model through a cloud marketplace, rather than a backend the gateway can
// select with the x-api-provider header. They are dropped from the manifest's
// provider list so nobody tries to route to "azure_openai".
var transportProviders = map[string]bool{
	"bedrock_anthropic": true,
	"vertex_anthropic":  true,
	"azure_anthropic":   true,
	"bedrock_openai":    true,
	"azure_openai":      true,
	"snowflake":         true,
	"together":          true,
}

// providers reads the model's routable backend list, preferring the global
// region override.
//
// apiProviders is the model's full backend set, but regionOverrides.global
// narrows it for the default region: GLM-5.2, for instance, lists
// ["baseten","mistral"] globally and is baseten outside the EU.
func providers(obj, platform string) []string {
	// Must stay nil-initialised: the branches below test for nil to mean
	// "no readable list here", which an empty slice would defeat.
	var raw []string
	if m := reGlobalPrv.FindStringSubmatch(obj); m != nil {
		raw = quotedStrings(m[1])
	}
	if raw == nil {
		if m := reAPIPrv.FindStringSubmatch(obj); m != nil {
			raw = quotedStrings(m[1])
		}
	}
	// Identifier-valued apiProviders (legacy native models) cannot be read
	// textually; the model's own platform is the route the gateway accepts.
	if raw == nil {
		if platform != "" {
			return []string{platform}
		}
		return []string{}
	}

	out := make([]string, 0, len(raw))
	for _, p := range raw {
		if transportProviders[p] {
			continue
		}
		out = append(out, p)
	}
	// A model reachable only through a marketplace transport is still reachable
	// through its platform's own API.
	if len(out) == 0 && platform != "" {
		return []string{platform}
	}
	return out
}

// splitRefs splits an array body into its members without breaking nested
// object literals: promotions:[{discount:0.5,startsAt:new Date(...)}] is one
// member, not four.
func splitRefs(body string) []string {
	var out []string
	for i := 0; i < len(body); {
		c := body[i]
		if c == ',' || c == ' ' {
			i++
			continue
		}
		if c == '{' {
			blk := matchBalanced([]byte(body), i)
			if blk == nil {
				break
			}
			out = append(out, string(blk))
			i += len(blk)
			continue
		}
		j := i
		for j < len(body) && body[j] != ',' {
			j++
		}
		if tok := strings.TrimSpace(body[i:j]); tok != "" {
			out = append(out, tok)
		}
		i = j
	}
	return out
}

func parsePromotions(obj string, defs map[string][][]byte) []Promotion {
	m := rePromos.FindStringSubmatch(obj)
	if m == nil {
		return nil
	}
	var out []Promotion
	for _, ref := range splitRefs(m[1]) {
		var literal string
		if strings.HasPrefix(ref, "{") {
			literal = ref
		} else {
			// Resolve an identifier only when exactly one definition with that
			// name exists; ambiguity is reported as "no promotion", not guessed.
			if !reIdent.MatchString(ref) {
				continue
			}
			candidates := defs[ref]
			if len(candidates) != 1 {
				continue
			}
			literal = string(candidates[0])
		}
		p := Promotion{
			Discount:  jnum(rePromoDiscount, literal),
			StartsAt:  jstr(rePromoStart, literal),
			ExpiresAt: jstr(rePromoExpires, literal),
			Label:     jstr(rePromoLabel, literal),
		}
		if p.Discount > 0 {
			out = append(out, p)
		}
	}
	return out
}

// nameOverrides supplies display names the bundle stores as JS identifier
// references, which cannot be read textually. Keep this list as short as
// possible; it exists only so a model never shows up nameless in the UI.
var nameOverrides = map[string]string{
	"auto": "Auto Model",
}

func parseModel(id string, obj []byte, defs map[string][][]byte) Model {
	s := string(obj)

	reasoning := Reasoning{Efforts: []string{}}
	if m := reEffortBlock.FindStringSubmatch(s); m != nil {
		// Read the supported list specifically; the block also carries the
		// default, which would otherwise be picked up as another effort level.
		if set := reEffortSet.FindStringSubmatch(m[1]); set != nil {
			for _, q := range reQuoted.FindAllStringSubmatch(set[1], -1) {
				reasoning.Efforts = append(reasoning.Efforts, q[1])
			}
		}
		reasoning.Default = jstr(reEffortDef, m[1])
	}

	provider := jstr(reProvider, s)
	// Media support is asymmetric in the registry, matching how the bundle
	// reads it: supportsImages is `images !== false`, so an absent field means
	// supported, while supportsPDFs is `pdf === true` and must be explicit.
	vision := true
	if img := reImages.FindStringSubmatch(s); img != nil {
		vision = img[1] == "!0" || img[1] == "true"
	}
	m := Model{
		ID:               id,
		Name:             jstr(reName, s),
		Provider:         provider,
		APIModelProvider: jstr(reAPIModelPrv, s),
		Providers:        providers(s, provider),
		QuotaPool:        jstr(reBillingPool, s),
		ServiceTier:      jstr(reTier, s),
		Vision:           vision,
		PDF:              jbool(rePDF, s),
		Reasoning:        reasoning,
		Cost: Cost{
			TokenMultiplier:     jnum(reTokenMult, s),
			OutputMultiplier:    jnum(reOutputMult, s),
			CacheReadMultiplier: jnum(reCacheMult, s),
		},
		CLIVisible: !strings.Contains(s, "availableInCLI:!1"),
	}
	if dep := reDeprecation.FindStringSubmatch(s); dep != nil {
		m.DeprecatedOn = jstr(reDepDate, dep[1])
		m.Replacement = jstr(reFallback, dep[1])
	}
	if m.QuotaPool == "" {
		m.QuotaPool = "standard"
	}
	if m.Name == "" {
		if override, ok := nameOverrides[id]; ok {
			m.Name = override
		} else {
			m.Name = id
		}
	}
	if mm := reAPIConfig.FindAllStringSubmatch(s, -1); len(mm) > 0 {
		pm := make(map[string]string, len(mm))
		for _, pair := range mm {
			pm[pair[1]] = pair[2]
		}
		m.ProviderModels = pm
	}
	m.Promotions = parsePromotions(s, defs)
	return m
}

// ---------- validation ----------

const minModels = 40

// requiredProviders guards against an extraction that silently loses a whole
// model family when the bundle layout changes.
var requiredProviders = []string{"anthropic", "openai", "google", "factory"}

// validate returns the problems found; empty means the manifest is trustworthy.
func validate(models []Model) []string {
	var problems []string
	if len(models) < minModels {
		problems = append(problems, fmt.Sprintf("only %d models extracted, expected at least %d", len(models), minModels))
	}

	seen := map[string]bool{}
	byProvider := map[string]int{}
	for _, m := range models {
		if seen[m.ID] {
			problems = append(problems, "duplicate id: "+m.ID)
		}
		seen[m.ID] = true
		byProvider[m.Provider]++

		if m.Name == "" {
			problems = append(problems, m.ID+": missing name")
		}
		if m.Cost.TokenMultiplier == 0 {
			problems = append(problems, m.ID+": missing cost.token_multiplier")
		}
		if len(m.Reasoning.Efforts) == 0 {
			problems = append(problems, m.ID+": missing reasoning efforts")
		}
		if len(m.Providers) == 0 {
			problems = append(problems, m.ID+": no providers")
		}
	}
	for _, p := range requiredProviders {
		if byProvider[p] == 0 {
			problems = append(problems, "no models for provider: "+p)
		}
	}
	return problems
}

// A model line in the CLI's help: two spaces, the id, a run of spaces, then the
// display name — which carries a "[Deprecated]" suffix on retired models.
var reHelpModel = regexp.MustCompile(`^\s{2}([a-z0-9][a-z0-9.\-]*)\s{2,}(.+)$`)

// helpModel is one model as the CLI itself lists it.
type helpModel struct {
	id         string
	deprecated bool
}

// modelsFromHelp reads the model ids out of `droid exec --help` output.
//
// The CLI prints its own selectable models there, which makes that list an
// independent statement of the catalog: if the extraction misses one of them,
// or disagrees about which are retired, the bundle layout probably changed and
// the manifest should not be trusted.
func modelsFromHelp(path string) ([]helpModel, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	text := string(raw)
	_, after, found := strings.Cut(text, "Available Models:")
	if !found {
		return nil, fmt.Errorf("%s has no \"Available Models:\" section", path)
	}
	section, _, _ := strings.Cut(after, "Model details:")

	var out []helpModel
	for _, line := range strings.Split(section, "\n") {
		m := reHelpModel.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		out = append(out, helpModel{
			id:         m[1],
			deprecated: strings.Contains(m[2], "[Deprecated]"),
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s lists no models", path)
	}
	return out, nil
}

// excludedModels are registry entries deliberately left out of the catalog.
//
// They are still parsed, so a rename or removal shows up in the logs instead of
// disappearing silently — but they are never written to the manifest.
var excludedModels = map[string]string{
	"auto":      "the Factory Router picks a model server-side; the gateway cannot describe, route or benchmark it",
	"auto-fast": "same as auto: a server-side router, not a model",
}

// ---------- main ----------

func main() {
	binary := flag.String("binary", "", "path to a droid binary (positional argument also accepted)")
	version := flag.String("version", "", "CLI version the binary reports, e.g. 0.238.0")
	out := flag.String("out", "-", "output path, or - for stdout")
	noTimestamp := flag.Bool("no-timestamp", false, "omit generated_at, so the file can be diffed across runs")
	crossCheck := flag.String("cross-check", "", "path to `droid exec --help` output; fails when a model it lists is missing from the extraction")

	// Go's flag package stops parsing at the first non-flag argument. Rotate a
	// leading bare path to the end so the binary can be given either way:
	// "extract-manifest <bin> --version X" or "--version X <bin>".
	args := os.Args[1:]
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		args = append(args[1:], args[0])
	}
	flag.CommandLine.Parse(args)

	binaryPath := *binary
	if binaryPath == "" {
		binaryPath = flag.Arg(0)
	}
	if binaryPath == "" || *version == "" {
		fmt.Fprintln(os.Stderr, "usage: extract-manifest <droid-binary> --version <x.y.z> [--out manifest.json]")
		os.Exit(2)
	}

	started := time.Now()
	data, err := os.ReadFile(binaryPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "read binary:", err)
		os.Exit(1)
	}

	entries := findEntries(data)
	defs := findPromotionDefs(data)
	fmt.Fprintf(os.Stderr, "scanned %.0f MB in %s: %d registry entries, %d promotion definitions\n",
		float64(len(data))/1e6, time.Since(started).Round(time.Millisecond), len(entries), len(defs))

	ids := make([]string, 0, len(entries))
	for id := range entries {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	models := make([]Model, 0, len(ids))
	for _, id := range ids {
		if reason, skip := excludedModels[id]; skip {
			fmt.Fprintf(os.Stderr, "skipping %s: %s\n", id, reason)
			continue
		}
		models = append(models, parseModel(id, entries[id], defs))
	}

	problems := validate(models)
	if *crossCheck != "" {
		listed, err := modelsFromHelp(*crossCheck)
		if err != nil {
			fmt.Fprintln(os.Stderr, "cross-check:", err)
			os.Exit(1)
		}
		byID := make(map[string]Model, len(models))
		for _, m := range models {
			byID[m.ID] = m
		}
		var missing, disagree []string
		deprecatedInHelp := 0
		for _, h := range listed {
			if _, skip := excludedModels[h.id]; skip {
				continue
			}
			m, ok := byID[h.id]
			if !ok {
				missing = append(missing, h.id)
				continue
			}
			if h.deprecated {
				deprecatedInHelp++
				if m.DeprecatedOn == "" {
					disagree = append(disagree, h.id)
				}
			}
		}
		fmt.Fprintf(os.Stderr,
			"cross-check: the CLI lists %d models (%d retired), %d missing from the extraction, %d retired there but not here\n",
			len(listed), deprecatedInHelp, len(missing), len(disagree))
		for _, id := range missing {
			problems = append(problems, "listed by the CLI but not extracted: "+id)
		}
		for _, id := range disagree {
			problems = append(problems, "the CLI marks as deprecated but the extraction has no date: "+id)
		}
	}
	if len(problems) > 0 {
		for _, p := range problems {
			fmt.Fprintln(os.Stderr, "validation:", p)
		}
		fmt.Fprintln(os.Stderr, "extraction looks incomplete; refusing to write a manifest")
		os.Exit(1)
	}

	manifest := Manifest{
		Schema: schemaVersion,
		Source: "droid-cli",
		CLI: CLIInfo{
			Version:   *version,
			UserAgent: "factory-cli/" + *version,
		},
		Models: models,
	}
	if !*noTimestamp {
		manifest.GeneratedAt = time.Now().UTC().Format("2006-01-02T15:04:05Z")
	}

	payload, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "encode manifest:", err)
		os.Exit(1)
	}
	payload = append(payload, '\n')

	if *out == "-" {
		os.Stdout.Write(payload)
		return
	}
	if err := os.WriteFile(*out, payload, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "write manifest:", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "wrote %s: %d models, %s total\n", *out, len(models), time.Since(started).Round(time.Millisecond))
}
