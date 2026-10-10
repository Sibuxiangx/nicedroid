package store

import (
	"math"
	"testing"
)

// close compares the unrounded TPS the function returns with an expectation.
func close(a, b float64) bool { return math.Abs(a-b) < 0.01 }

func TestComputeTPSNonStreamingUsesFullLatency(t *testing.T) {
	if got := ComputeTPS(16, 2000, 2000, false); !close(got, 8) {
		t.Fatalf("got %v, want 8", got)
	}
}

func TestComputeTPSBufferedStreamFallsBackToEndToEnd(t *testing.T) {
	// A buffered flush puts TTFT next to the total latency. Dividing by the
	// leftover sliver used to report 391 tok/s for a request that took 2.06s;
	// end-to-end is the only honest figure left.
	if got := ComputeTPS(27, 2064, 1995, true); !close(got, 13.08) {
		t.Fatalf("got %v, want 13.08", got)
	}
	// Just under the old 50ms floor, which used to fall back anyway.
	if got := ComputeTPS(27, 2170, 2143, true); !close(got, 12.44) {
		t.Fatalf("got %v, want 12.44", got)
	}
}

func TestComputeTPSGenuineStreamSubtractsTTFT(t *testing.T) {
	// A long generation whose TTFT is a minority of the request: the window is
	// real, so the subtraction stays.
	if got := ComputeTPS(215, 3258, 2354, true); !close(got, 237.83) {
		t.Fatalf("got %v, want 237.83", got)
	}
	if got := ComputeTPS(91, 2487, 1802, true); !close(got, 132.85) {
		t.Fatalf("got %v, want 132.85", got)
	}
}

func TestComputeTPSZeroWhenNothingToMeasure(t *testing.T) {
	if got := ComputeTPS(0, 2000, 500, true); got != 0 {
		t.Fatalf("got %v, want 0", got)
	}
	if got := ComputeTPS(10, 0, 0, true); got != 0 {
		t.Fatalf("got %v, want 0", got)
	}
}
