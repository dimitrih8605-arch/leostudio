package service

// AspectSize maps an aspect ratio string to a (width, height) tuple, mirroring
// ASPECT_TO_SIZE in app/leonardo_service.py.
var AspectSize = map[string][2]int{
	"16:9": {2752, 1536},
	"9:16": {1536, 2752},
	"1:1":  {1536, 1536},
	"4:3":  {2048, 1536},
}

// IsKnownAspect reports whether the ratio is in the supported list.
func IsKnownAspect(ratio string) bool {
	_, ok := AspectSize[ratio]
	return ok
}

// ResolveSize returns (width, height) for the given aspect ratio,
// defaulting to 1:1 when the ratio is unknown.
func ResolveSize(ratio string) (int, int) {
	if size, ok := AspectSize[ratio]; ok {
		return size[0], size[1]
	}
	def := AspectSize["1:1"]
	return def[0], def[1]
}

// SizeAliasToAspect mirrors the size alias map in main.py used by
// _resolve_generation_request.
var SizeAliasToAspect = map[string]string{
	"1344x768":  "16:9",
	"768x1344":  "9:16",
	"2752x1536": "16:9",
	"1536x2752": "9:16",
	"1024x1024": "1:1",
	"1536x1536": "1:1",
	"1152x896":  "4:3",
	"2048x1536": "4:3",
}
