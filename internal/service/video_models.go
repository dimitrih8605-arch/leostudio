package service

import "strings"

// VideoModel describes one supported video model and its constraints.
type VideoModel struct {
	Slug             string                            // value sent as `model` to the Generate mutation
	DefaultMode      string                            // RESOLUTION_480 | RESOLUTION_720 | RESOLUTION_1080
	SupportedModes   []string                          // accepted resolutions for this model
	Dimensions       map[string]map[string][2]int      // mode -> aspect_ratio -> (width, height)
	DurationOptions  []int                             // allowed duration values in seconds
	DefaultDuration  int                               // default duration value
	SupportsAudio    bool
	SupportsRefImage bool                              // whether reference image (start_frame) is supported
	DefaultAspect    string                            // fallback aspect when client doesn't supply one
}

// videoDims is the shared dimension table reused by all video models.
var videoDims = map[string]map[string][2]int{
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
}

// VideoModels is the registry of supported slugs. Order is significant: the
// first entry is the implicit default model.
var VideoModels = []VideoModel{
	{
		Slug: "seedance-2.0",
		DefaultMode: "RESOLUTION_480",
		SupportedModes: []string{
			"RESOLUTION_480",
			"RESOLUTION_720",
			"RESOLUTION_1080",
		},
		Dimensions:       videoDims,
		DurationOptions:  []int{4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15},
		DefaultDuration:  8,
		SupportsAudio:    true,
		SupportsRefImage: true,
		DefaultAspect:    "16:9",
	},
	{
		Slug: "seedance-2.0-fast",
		DefaultMode: "RESOLUTION_480",
		SupportedModes: []string{
			"RESOLUTION_480",
			"RESOLUTION_720",
			"RESOLUTION_1080",
		},
		Dimensions:       videoDims,
		DurationOptions:  []int{4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15},
		DefaultDuration:  8,
		SupportsAudio:    true,
		SupportsRefImage: true,
		DefaultAspect:    "16:9",
	},
	{
		Slug: "seedance-2.0-mini",
		DefaultMode: "RESOLUTION_480",
		SupportedModes: []string{
			"RESOLUTION_480",
			"RESOLUTION_720",
		},
		Dimensions:       videoDims,
		DurationOptions:  []int{4, 5, 6, 7, 8},
		DefaultDuration:  6,
		SupportsAudio:    false,
		SupportsRefImage: false,
		DefaultAspect:    "16:9",
	},
	{
		Slug: "happy-horse-1.1",
		DefaultMode: "RESOLUTION_480",
		SupportedModes: []string{
			"RESOLUTION_480",
			"RESOLUTION_720",
			"RESOLUTION_1080",
		},
		Dimensions:       videoDims,
		DurationOptions:  []int{6, 10},
		DefaultDuration:  6,
		SupportsAudio:    false,
		SupportsRefImage: true,
		DefaultAspect:    "16:9",
	},
	{
		Slug: "hailuo-2_3",
		DefaultMode: "RESOLUTION_720",
		SupportedModes: []string{
			"RESOLUTION_720",
			"RESOLUTION_1080",
		},
		Dimensions:       videoDims,
		DurationOptions:  []int{6, 10},
		DefaultDuration:  6,
		SupportsAudio:    false,
		SupportsRefImage: false,
		DefaultAspect:    "16:9",
	},
	{
		Slug: "hailuo-2.3-fast",
		DefaultMode: "RESOLUTION_720",
		SupportedModes: []string{
			"RESOLUTION_720",
			"RESOLUTION_1080",
		},
		Dimensions:       videoDims,
		DurationOptions:  []int{6, 10},
		DefaultDuration:  6,
		SupportsAudio:    false,
		SupportsRefImage: false,
		DefaultAspect:    "16:9",
	},
	{
		Slug: "kling-3.0-turbo",
		DefaultMode: "RESOLUTION_720",
		SupportedModes: []string{
			"RESOLUTION_720",
			"RESOLUTION_1080",
		},
		Dimensions:       videoDims,
		DurationOptions:  []int{6, 10},
		DefaultDuration:  6,
		SupportsAudio:    true,
		SupportsRefImage: false,
		DefaultAspect:    "16:9",
	},
	{
		Slug: "grok-imagine-1.5",
		DefaultMode: "RESOLUTION_720",
		SupportedModes: []string{
			"RESOLUTION_720",
			"RESOLUTION_1080",
		},
		Dimensions:       videoDims,
		DurationOptions:  []int{6, 10},
		DefaultDuration:  6,
		SupportsAudio:    false,
		SupportsRefImage: false,
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
	if input == "" {
		return m.DefaultMode
	}
	candidates := []string{
		"RESOLUTION_" + input,
		"RESOLUTION_" + input + "P",
		"RESOLUTION_" + input + "0",
	}
	return choose(m, input, candidates...)
}

func choose(m *VideoModel, candidate string, aliases ...string) string {
	for _, a := range aliases {
		for _, mode := range m.SupportedModes {
			if strings.EqualFold(mode, a) {
				return mode
			}
		}
	}
	return m.DefaultMode
}

// ResolveDimensions returns (width, height) for the requested mode + aspect.
func (m *VideoModel) ResolveDimensions(mode, aspect string) (int, int) {
	if m == nil {
		return 0, 0
	}
	if m.Dimensions == nil {
		return 0, 0
	}
	byAspect, ok := m.Dimensions[mode]
	if !ok {
		return 0, 0
	}
	dims, ok := byAspect[aspect]
	if !ok {
		// fall back to default aspect
		dims, ok = byAspect[m.DefaultAspect]
		if !ok {
			return 0, 0
		}
	}
	return dims[0], dims[1]
}

// ClampDuration enforces the supported duration values for the model.
func (m *VideoModel) ClampDuration(req int) int {
	if m == nil || len(m.DurationOptions) == 0 {
		return req
	}
	if req <= 0 {
		return m.DefaultDuration
	}
	for _, d := range m.DurationOptions {
		if d == req {
			return req
		}
	}
	// nearest
	best := m.DurationOptions[0]
	for _, d := range m.DurationOptions {
		if abs(d-req) < abs(best-req) {
			best = d
		}
	}
	return best
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
