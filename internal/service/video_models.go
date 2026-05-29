package service

import "strings"

// VideoModel describes one supported video model and its constraints.
//
// We only commit to Seedance for now; Veo and Kling can be added later by
// appending entries here once their request shapes are captured from the
// browser (different endpoints / different parameter keys per docs).
type VideoModel struct {
	Slug             string         // value sent as `model` to the Generate mutation
	DefaultMode      string         // RESOLUTION_480 | RESOLUTION_720 | RESOLUTION_1080
	SupportedModes   []string       // accepted resolutions for this model
	Dimensions       map[string]map[string][2]int // mode -> aspect_ratio -> (width, height)
	DurationOptions  []int          // allowed duration values in seconds
	DefaultDuration  int            // default duration value
	SupportsAudio    bool
	SupportsRefImage bool           // whether reference image (start_frame) is supported
	DefaultAspect    string         // fallback aspect when client doesn't supply one
}

// VideoModels is the registry of supported slugs. Order is significant: the
// first entry is the implicit default model.
var VideoModels = []VideoModel{
	{
		Slug:        "seedance-2.0",
		DefaultMode: "RESOLUTION_480",
		SupportedModes: []string{
			"RESOLUTION_480",
			"RESOLUTION_720",
			"RESOLUTION_1080",
		},
		Dimensions: map[string]map[string][2]int{
			"RESOLUTION_480": {
				"16:9": {864, 496},
				"9:16": {496, 864},
				"1:1":  {640, 640},
				"4:3":  {752, 560},
				"3:4":  {560, 752},
				"21:9": {992, 432},
				"9:21": {432, 992},
			},
			"RESOLUTION_720": {
				"16:9": {1280, 720},
				"9:16": {720, 1280},
				"1:1":  {960, 960},
				"4:3":  {1112, 834},
				"3:4":  {834, 1112},
				"21:9": {1470, 630},
			},
			"RESOLUTION_1080": {
				"16:9": {1920, 1080},
				"9:16": {1080, 1920},
				"1:1":  {1080, 1080},
				"4:3":  {1440, 1080},
				"3:4":  {834, 1112},
				"21:9": {2520, 1080},
				"9:21": {1080, 2520},
			},
		},
		DurationOptions:  []int{4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15},
		DefaultDuration:  8,
		SupportsAudio:    true,
		SupportsRefImage: true,
		DefaultAspect:    "16:9",
	},
	{
		Slug:        "seedance-2.0-fast",
		DefaultMode: "RESOLUTION_480",
		SupportedModes: []string{
			"RESOLUTION_480",
			"RESOLUTION_720",
			"RESOLUTION_1080",
		},
		// Same dimension table as Seedance 2.0 per docs.
		Dimensions: map[string]map[string][2]int{
			"RESOLUTION_480": {
				"16:9": {864, 496},
				"9:16": {496, 864},
				"1:1":  {640, 640},
				"4:3":  {752, 560},
				"3:4":  {560, 752},
				"21:9": {992, 432},
				"9:21": {432, 992},
			},
			"RESOLUTION_720": {
				"16:9": {1280, 720},
				"9:16": {720, 1280},
				"1:1":  {960, 960},
				"4:3":  {1112, 834},
				"3:4":  {834, 1112},
				"21:9": {1470, 630},
			},
			"RESOLUTION_1080": {
				"16:9": {1920, 1080},
				"9:16": {1080, 1920},
				"1:1":  {1080, 1080},
				"4:3":  {1440, 1080},
				"3:4":  {834, 1112},
				"21:9": {2520, 1080},
				"9:21": {1080, 2520},
			},
		},
		DurationOptions:  []int{4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15},
		DefaultDuration:  8,
		SupportsAudio:    true,
		SupportsRefImage: true,
		DefaultAspect:    "16:9",
	},
}

// LookupVideoModel returns the model definition for a slug, or nil when not
// supported. Slug matching is case-insensitive to be forgiving on inputs.
func LookupVideoModel(slug string) *VideoModel {
	q := strings.ToLower(strings.TrimSpace(slug))
	for i := range VideoModels {
		if strings.ToLower(VideoModels[i].Slug) == q {
			return &VideoModels[i]
		}
	}
	return nil
}

// DefaultVideoModel returns the implicit default model (first entry).
func DefaultVideoModel() *VideoModel {
	if len(VideoModels) == 0 {
		return nil
	}
	return &VideoModels[0]
}

// ResolveResolution maps a friendly alias to the Leonardo enum. Returns
// the model default when input is empty/unknown.
func (m *VideoModel) ResolveResolution(input string) string {
	if m == nil {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(input)) {
	case "":
		return m.DefaultMode
	case "480p", "480", "resolution_480", "sd":
		return choose(m, "RESOLUTION_480")
	case "720p", "720", "resolution_720", "hd":
		return choose(m, "RESOLUTION_720")
	case "1080p", "1080", "resolution_1080", "fhd":
		return choose(m, "RESOLUTION_1080")
	}
	return choose(m, strings.ToUpper(input))
}

func choose(m *VideoModel, candidate string) string {
	for _, supported := range m.SupportedModes {
		if supported == candidate {
			return supported
		}
	}
	return m.DefaultMode
}

// ResolveDimensions returns (width, height) for the requested mode + aspect.
// Falls back to model.DefaultAspect when the requested aspect is not supported.
// Returns (0, 0) when the mode itself is unknown.
func (m *VideoModel) ResolveDimensions(mode, aspect string) (int, int) {
	if m == nil {
		return 0, 0
	}
	table, ok := m.Dimensions[mode]
	if !ok {
		return 0, 0
	}
	if aspect == "" {
		aspect = m.DefaultAspect
	}
	if dims, ok := table[aspect]; ok {
		return dims[0], dims[1]
	}
	if dims, ok := table[m.DefaultAspect]; ok {
		return dims[0], dims[1]
	}
	return 0, 0
}

// ClampDuration enforces the supported duration values for the model. Returns
// the requested value when valid, otherwise the closest allowed value (or the
// default when input is non-positive).
func (m *VideoModel) ClampDuration(req int) int {
	if m == nil {
		return 0
	}
	if req <= 0 {
		return m.DefaultDuration
	}
	for _, allowed := range m.DurationOptions {
		if allowed == req {
			return req
		}
	}
	// Snap to the highest allowed value <= req, or the smallest available.
	best := m.DurationOptions[0]
	for _, allowed := range m.DurationOptions {
		if allowed <= req && allowed > best {
			best = allowed
		}
	}
	return best
}
