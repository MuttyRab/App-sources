package accel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// benchPrompt is plain text (no chat template) long enough to measure prompt
// speed (~450 tokens) and to give a deterministic continuation.
const benchPrompt = `The following is an encyclopedia article about the water cycle.

The water cycle, also known as the hydrologic cycle, describes the continuous movement of water on, above and below the surface of the Earth. The mass of water on Earth remains fairly constant over time, but the partitioning of the water into the major reservoirs of ice, fresh water, salt water and atmospheric water is variable depending on a wide range of climatic variables. The water moves from one reservoir to another, such as from river to ocean, or from the ocean to the atmosphere, by the physical processes of evaporation, transpiration, condensation, precipitation, infiltration, surface runoff, and subsurface flow. In doing so, the water goes through different forms: liquid, solid (ice) and vapor. The ocean plays a key role in the water cycle as it is the source of most of the evaporated water.

The water cycle involves the exchange of energy, which leads to temperature changes. When water evaporates, it takes up energy from its surroundings and cools the environment. When it condenses, it releases energy and warms the environment. These heat exchanges influence climate. The evaporative phase of the cycle purifies water, which then replenishes the land with freshwater. The flow of liquid water and ice transports minerals across the globe. It is also involved in reshaping the geological features of the Earth, through processes including erosion and sedimentation. The water cycle is also essential for the maintenance of most life and ecosystems on the planet.

Sunlight drives the water cycle. Energy from the Sun heats the oceans, and water evaporates as water vapor into the air. Some ice and snow sublimate directly into water vapor. Plants release water vapor from their leaves in a process called transpiration. Rising air currents take the vapor up into the atmosphere, where cooler temperatures cause it to condense into clouds. Air currents move water vapor around the globe. Cloud particles collide, grow, and fall out of the upper atmospheric layers as precipitation. Some precipitation falls as snow and can accumulate as ice caps and glaciers, which can store frozen water for thousands of years.

In summary, the three most important steps of the water cycle are`

// Run measures prompt and generation speed of a ready llama-server at baseURL
// and returns its greedy continuation of a fixed prompt.
func Run(ctx context.Context, baseURL string, nPredict int) (*Bench, error) {
	body, _ := json.Marshal(map[string]any{
		"prompt":       benchPrompt,
		"n_predict":    nPredict,
		"temperature":  0,
		"top_k":        1,
		"cache_prompt": false,
		"samplers":     []string{"top_k"},
		"stream":       false,
	})
	ctx, cancel := context.WithTimeout(ctx, 6*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", baseURL+"/completion", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("engine answered %s", resp.Status)
	}
	var out struct {
		Content string `json:"content"`
		Timings struct {
			PromptN   int     `json:"prompt_n"`
			PromptPS  float64 `json:"prompt_per_second"`
			Predicted int     `json:"predicted_n"`
			PredPS    float64 `json:"predicted_per_second"`
		} `json:"timings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	b := &Bench{PromptTPS: out.Timings.PromptPS, GenTPS: out.Timings.PredPS, Text: out.Content}
	b.Seconds = TurnSeconds(b.PromptTPS, b.GenTPS)
	return b, nil
}
