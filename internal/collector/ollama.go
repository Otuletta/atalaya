package collector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

// OllamaModel is a model loaded in memory.
type OllamaModel struct {
	Name      string    `json:"name"`
	SizeBytes int64     `json:"size_bytes"`
	VRAMBytes int64     `json:"vram_bytes"`
	Params    string    `json:"params,omitempty"`
	Quant     string    `json:"quantization,omitempty"`
	ExpiresAt time.Time `json:"expires_at"`
}

// OllamaSnapshot is the result of the ollama collector.
type OllamaSnapshot struct {
	Available bool          `json:"available"`
	Note      string        `json:"note,omitempty"`
	Models    []OllamaModel `json:"models"`
}

// Ollama lists loaded models with GET /api/ps.
type Ollama struct {
	Host   string
	Every  time.Duration
	Client *http.Client
}

func (o *Ollama) Name() string            { return "ollama" }
func (o *Ollama) Interval() time.Duration { return o.Every }

func (o *Ollama) Collect(ctx context.Context) (Snapshot, error) {
	client := o.Client
	if client == nil {
		client = &http.Client{Timeout: 3 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(o.Host, "/")+"/api/ps", nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		var op *net.OpError
		if errors.As(err, &op) && op.Op == "dial" {
			return OllamaSnapshot{Note: "Ollama no está corriendo"}, nil
		}
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama: %s", resp.Status)
	}
	var body struct {
		Models []struct {
			Name      string    `json:"name"`
			Size      int64     `json:"size"`
			SizeVRAM  int64     `json:"size_vram"`
			ExpiresAt time.Time `json:"expires_at"`
			Details   struct {
				ParameterSize     string `json:"parameter_size"`
				QuantizationLevel string `json:"quantization_level"`
			} `json:"details"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("ollama: %w", err)
	}
	snap := OllamaSnapshot{Available: true, Models: []OllamaModel{}}
	for _, m := range body.Models {
		snap.Models = append(snap.Models, OllamaModel{
			Name: m.Name, SizeBytes: m.Size, VRAMBytes: m.SizeVRAM, ExpiresAt: m.ExpiresAt,
			Params: m.Details.ParameterSize, Quant: m.Details.QuantizationLevel,
		})
	}
	return snap, nil
}
