// gen-opencode writes an OpenCode provider config from the model catalog.
//
// OpenCode needs every model listed by ID in its config before it will offer
// it, and the catalog already knows those IDs — so the list is generated, not
// typed. Models are filtered to the ones a coding agent can actually use:
// current in the CLI, and not Gemini, which the proxy only forwards in
// Google's native format and therefore cannot serve over chat completions.
//
// Usage:
//
//	go run ./tools/gen-opencode [-manifest manifest.json] [-base https://host] [-out opencode.json]
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type manifestModel struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Provider    string   `json:"provider"`
	Providers   []string `json:"providers"`
	Deprecated  string   `json:"deprecated_on"`
	Visible     bool     `json:"cli_visible"`
	Passthrough bool     `json:"passthrough"`
	Reasoning   struct {
		Efforts []string `json:"efforts"`
	} `json:"reasoning"`
}

// family mirrors config.familyFor: the wire format the proxy speaks upstream
// for this model, which decides which AI SDK package OpenCode should load.
func (m manifestModel) family() string {
	switch m.Provider {
	case "anthropic":
		return "messages"
	case "google":
		return "google"
	case "openai":
		return "responses"
	default:
		return "chat"
	}
}

type manifestFile struct {
	Models []manifestModel `json:"models"`
}

type opencodeModel struct {
	Name      string `json:"name"`
	ToolCall  bool   `json:"tool_call"`
	Reasoning bool   `json:"reasoning,omitempty"`
	NPM       string `json:"npm,omitempty"`
}

type opencodeProvider struct {
	NPM     string                   `json:"npm"`
	Name    string                   `json:"name"`
	Options map[string]string        `json:"options"`
	Models  map[string]opencodeModel `json:"models"`
}

type opencodeConfig struct {
	Schema   string                      `json:"$schema"`
	Provider map[string]opencodeProvider `json:"provider"`
}

func main() {
	manifest := flag.String("manifest", "manifest.json", "path to manifest.json")
	base := flag.String("base", "https://nicedroid.linota.cn/v1", "proxy base URL for OpenAI-compatible calls")
	out := flag.String("out", "opencode.json", "output path")
	flag.Parse()

	raw, err := os.ReadFile(*manifest)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read manifest: %v\n", err)
		os.Exit(1)
	}
	var mf manifestFile
	if err := json.Unmarshal(raw, &mf); err != nil {
		fmt.Fprintf(os.Stderr, "parse manifest: %v\n", err)
		os.Exit(1)
	}

	models := map[string]opencodeModel{}
	gemini := map[string]opencodeModel{}
	for _, m := range mf.Models {
		if !m.Visible || m.Deprecated != "" {
			continue
		}
		entry := opencodeModel{
			Name:     m.Name,
			ToolCall: true,
			Reasoning: len(m.Reasoning.Efforts) > 1 ||
				(len(m.Reasoning.Efforts) == 1 && m.Reasoning.Efforts[0] != "off"),
		}
		switch m.family() {
		case "google":
			// Gemini is served in Google's native format on its own base URL,
			// so it belongs to its own provider with the Google SDK.
			gemini[m.ID] = entry
		default:
			// OpenAI-family models are served over /v1/responses upstream,
			// which the openai-compatible SDK does not speak; the openai one
			// does. Everything else rides chat completions.
			if m.family() == "responses" {
				entry.NPM = "@ai-sdk/openai"
			}
			models[m.ID] = entry
		}
	}
	if len(models) == 0 && len(gemini) == 0 {
		fmt.Fprintln(os.Stderr, "no callable models in the catalog")
		os.Exit(1)
	}

	provider := opencodeProvider{
		NPM:  "@ai-sdk/openai-compatible",
		Name: "NiceDroid",
		Options: map[string]string{
			"baseURL": *base,
			"apiKey":  "{env:NICEDROID_API_KEY}",
		},
		Models: models,
	}
	all := map[string]opencodeProvider{"nicedroid": provider}
	if len(gemini) > 0 {
		// The Google SDK appends /models/<model>:generateContent itself, so it
		// wants the base the proxy serves that tree under.
		gb := strings.TrimSuffix(*base, "/v1") + "/v1beta"
		all["nicedroid-gemini"] = opencodeProvider{
			NPM:  "@ai-sdk/google",
			Name: "NiceDroid Gemini",
			Options: map[string]string{
				"baseURL": gb,
				"apiKey":  "{env:NICEDROID_API_KEY}",
			},
			Models: gemini,
		}
	}

	cfg := opencodeConfig{
		Schema:   "https://opencode.ai/config.json",
		Provider: all,
	}

	enc, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "encode: %v\n", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "create dir: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(*out, append(enc, '\n'), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write: %v\n", err)
		os.Exit(1)
	}

	ids := make([]string, 0, len(models)+len(gemini))
	for id := range models {
		ids = append(ids, id)
	}
	for id := range gemini {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	fmt.Printf("wrote %s with %d models (%s)\n", *out, len(ids), strings.Join(ids, ", "))
}
