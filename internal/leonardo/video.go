package leonardo

import (
	"fmt"
	"strings"
	"time"
)

// VideoGuidanceImage describes one entry inside parameters.guidances.start_frame
// or parameters.guidances.end_frame as observed in the captured Seedance flow.
type VideoGuidanceImage struct {
	ID   string // Leonardo init image id (UUID)
	Type string // UPLOADED or GENERATED
}

// VideoInput collects parameters needed to submit a Seedance / video generation
// via the Generate mutation. The structure mirrors the body captured from
// app.leonardo.ai for Seedance 2.0:
//
//	{
//	  "model": "seedance-2.0",
//	  "public": true,
//	  "parameters": { width, height, duration, mode, motion_has_audio,
//	                  quantity, seed, prompt, guidances? }
//	}
type VideoInput struct {
	ModelSlug      string // e.g. "seedance-2.0", "seedance-2.0-fast"
	Prompt         string
	Width          int
	Height         int
	DurationSec    int
	Mode           string // RESOLUTION_480 | RESOLUTION_720 | RESOLUTION_1080
	HasAudio       bool
	Seed           int
	Quantity       int
	StartFrame     *VideoGuidanceImage // image-to-video start frame (optional)
	EndFrame       *VideoGuidanceImage // optional, requires StartFrame
	PromptEnhance  string              // ON | OFF | "" (omit)
	Public         bool
}

// VideoMedia is one generated video entry returned by GetVideoURLs.
type VideoMedia struct {
	GeneratedImageID string // id of the generated_images row that owns the video
	MP4URL           string // motionMP4URL — primary playable mp4
	GIFURL           string // motionGIFURL — preview gif (sering null)
	ThumbnailURL     string // url — frame thumbnail jpg
	Width            int
	Height           int
}

// CreateVideoGeneration submits the Seedance/video Generate mutation and
// returns the generationId. The mutation accepts the same envelope as image
// generation; only the inner `parameters` shape differs.
func (c *Client) CreateVideoGeneration(token string, in VideoInput) (string, error) {
	if strings.TrimSpace(in.ModelSlug) == "" {
		return "", fmt.Errorf("leonardo: video model slug is required")
	}
	if strings.TrimSpace(in.Prompt) == "" {
		return "", fmt.Errorf("leonardo: video prompt is required")
	}

	quantity := in.Quantity
	if quantity < 1 {
		quantity = 1
	}
	seed := in.Seed
	if seed == 0 {
		// Captured payload uses -1 to request a random seed; keep that behaviour.
		seed = -1
	}

	params := map[string]any{
		"width":            in.Width,
		"height":           in.Height,
		"duration":         in.DurationSec,
		"mode":             in.Mode,
		"motion_has_audio": in.HasAudio,
		"quantity":         quantity,
		"prompt":           strings.TrimSpace(in.Prompt),
		"seed":             seed,
	}

	// Reference image guidance: start_frame and optional end_frame.
	// The schema requires guidances.start_frame to be an array even when
	// only one image is provided. end_frame requires start_frame to be set.
	if in.StartFrame != nil && in.StartFrame.ID != "" {
		guidances := map[string]any{
			"start_frame": []map[string]any{
				{
					"image": map[string]any{
						"id":   in.StartFrame.ID,
						"type": orDefault(in.StartFrame.Type, "UPLOADED"),
					},
				},
			},
		}
		if in.EndFrame != nil && in.EndFrame.ID != "" {
			guidances["end_frame"] = []map[string]any{
				{
					"image": map[string]any{
						"id":   in.EndFrame.ID,
						"type": orDefault(in.EndFrame.Type, "UPLOADED"),
					},
				},
			}
		}
		params["guidances"] = guidances
	}

	if pe := strings.TrimSpace(in.PromptEnhance); pe != "" {
		params["prompt_enhance"] = pe
	}

	op := gqlPayload{
		OperationName: "Generate",
		Variables: map[string]any{
			"request": map[string]any{
				"model":      in.ModelSlug,
				"public":     in.Public,
				"parameters": params,
			},
		},
		Query: `mutation Generate($request: CreateGenerationRequest!) {
  generate(request: $request) {
    apiCreditCost
    generationId
    __typename
  }
}`,
	}

	resp, err := c.gql(token, op)
	if err != nil {
		return "", err
	}
	data, _ := resp["data"].(map[string]any)
	gen, _ := data["generate"].(map[string]any)
	if gen != nil {
		if id, ok := gen["generationId"].(string); ok && id != "" {
			return id, nil
		}
	}
	if msg := GraphQLErrorMessage(resp); msg != "" {
		return "", fmt.Errorf("%s", msg)
	}
	return "", fmt.Errorf("leonardo: video generate returned no generationId")
}

// GetVideoURLs reads the completed generation feed and extracts the motion
// media (MP4 + thumbnail + dimensions) for each generated image entry.
//
// The captured response shape (see gen.txt) places motionMP4URL alongside the
// regular jpg `url` inside generated_images. We use a focused query that only
// asks for those fields to keep the payload small.
func (c *Client) GetVideoURLs(token, genID string) ([]VideoMedia, error) {
	op := gqlPayload{
		OperationName: "GetVideoGenerationFeed",
		Variables: map[string]any{
			"where": map[string]any{"id": map[string]any{"_eq": genID}},
			"limit": 1,
		},
		Query: `query GetVideoGenerationFeed($where: generations_bool_exp = {}, $limit: Int) {
  generations(where: $where, limit: $limit) {
    id
    status
    motionDurationSeconds
    motionGenerationResolution
    generated_images(order_by: [{url: desc}]) {
      id
      url
      motionMP4URL
      motionGIFURL
      image_width
      image_height
      __typename
    }
    __typename
  }
}`,
	}
	resp, err := c.gql(token, op)
	if err != nil {
		return nil, err
	}
	data, _ := resp["data"].(map[string]any)
	gens, _ := data["generations"].([]any)
	if len(gens) == 0 {
		return nil, nil
	}
	first, _ := gens[0].(map[string]any)
	images, _ := first["generated_images"].([]any)

	out := make([]VideoMedia, 0, len(images))
	for _, item := range images {
		obj, _ := item.(map[string]any)
		if obj == nil {
			continue
		}
		mp4, _ := obj["motionMP4URL"].(string)
		if mp4 == "" {
			// Skip rows that haven't produced an mp4 yet.
			continue
		}
		gif, _ := obj["motionGIFURL"].(string)
		thumb, _ := obj["url"].(string)
		id, _ := obj["id"].(string)

		out = append(out, VideoMedia{
			GeneratedImageID: id,
			MP4URL:           mp4,
			GIFURL:           gif,
			ThumbnailURL:     thumb,
			Width:            asInt(obj["image_width"]),
			Height:           asInt(obj["image_height"]),
		})
	}
	return out, nil
}

// VideoCompletion is the result of WaitForVideoCompletion.
type VideoCompletion struct {
	Success bool
	Media   []VideoMedia
	Error   string
}

// WaitForVideoCompletion polls until at least one motionMP4URL is available.
// Status follows the same enum as image generation (COMPLETE = success).
func (c *Client) WaitForVideoCompletion(token, genID string, timeout, pollInterval time.Duration) VideoCompletion {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		status, err := c.PollStatus(token, genID)
		if err == nil {
			switch status {
			case "COMPLETE", "COMPLETED":
				media, _ := c.GetVideoURLs(token, genID)
				if len(media) > 0 {
					return VideoCompletion{Success: true, Media: media}
				}
				// Status flipped to COMPLETE but mp4 not indexed yet — give it
				// one more interval before declaring success or timeout.
			case "FAILED", "ERROR":
				return VideoCompletion{Success: false, Error: "video generation failed"}
			}
		}
		time.Sleep(pollInterval)
	}

	if media, err := c.GetVideoURLs(token, genID); err == nil && len(media) > 0 {
		return VideoCompletion{Success: true, Media: media}
	}
	return VideoCompletion{Success: false, Error: "video generation timeout"}
}

func orDefault(v, fallback string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return fallback
	}
	return v
}

func asInt(v any) int {
	switch t := v.(type) {
	case float64:
		return int(t)
	case int:
		return t
	case int64:
		return int(t)
	}
	return 0
}
