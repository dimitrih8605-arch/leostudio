package leonardo

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"path/filepath"
	"strings"
	"time"
)

// UserInfo is the minimal user profile used by the cookie pool.
type UserInfo struct {
	Email  string
	Tokens int64
}

// GenerationResult is returned by WaitForCompletion.
type GenerationResult struct {
	Success bool
	Images  []string
	Error   string
}

// GenerateInput collects parameters for create_generation.
type GenerateInput struct {
	Prompt       string
	ModelID      string
	Width        int
	Height       int
	Quantity     int
	InitImageIDs []string
	SDVersion    string
	StyleID      string // Leonardo style UUID, empty = use default (Dynamic)
}

// GetUserInfo returns email + total tokens for the bearer token.
// Mirrors GetUserDetails -> GetTokenBalance fallback in Python.
func (c *Client) GetUserInfo(token string) (UserInfo, error) {
	cognitoSub, tokenEmail := "", ""
	if payload := DecodeJWTPayload(token); payload != nil {
		if v, ok := payload["sub"].(string); ok {
			cognitoSub = v
		}
		if v, ok := payload["email"].(string); ok {
			tokenEmail = strings.TrimSpace(v)
		}
	}

	primary := gqlPayload{
		OperationName: "GetUserDetails",
		Variables:     map[string]any{"userSub": cognitoSub},
		Query: `query GetUserDetails($userSub: String) {
  users(where: {user_details: {cognitoId: {_eq: $userSub}}}) {
    id
    user_details {
      subscriptionTokens paidTokens rolloverTokens auth0Email __typename
    }
    __typename
  }
}`,
	}

	var lastError string

	if resp, err := c.gql(token, primary); err != nil {
		lastError = err.Error()
	} else {
		users, _ := resp["data"].(map[string]any)["users"].([]any)
		if len(users) > 0 {
			user, _ := users[0].(map[string]any)
			details := userDetailsFirst(user)
			if details != nil {
				return userInfoFromDetails(details, tokenEmail), nil
			}
		}
		if msg := GraphQLErrorMessage(resp); msg != "" {
			lastError = msg
		}
	}

	fallback := gqlPayload{
		OperationName: "GetTokenBalance",
		Variables:     map[string]any{},
		Query:         "query GetTokenBalance { user_details { subscriptionTokens paidTokens rolloverTokens auth0Email __typename } }",
	}
	resp, err := c.gql(token, fallback)
	if err != nil {
		if lastError == "" {
			lastError = err.Error()
		}
		return UserInfo{}, fmt.Errorf("leonardo: get user info: %s", lastError)
	}
	data, _ := resp["data"].(map[string]any)
	details, _ := data["user_details"].([]any)
	if len(details) == 0 {
		if msg := GraphQLErrorMessage(resp); msg != "" {
			lastError = msg
		}
		if lastError == "" {
			lastError = ErrNoUserDetails.Error()
		}
		return UserInfo{}, fmt.Errorf("leonardo: get user info: %s", lastError)
	}
	first, _ := details[0].(map[string]any)
	return userInfoFromDetails(first, tokenEmail), nil
}

func userDetailsFirst(user map[string]any) map[string]any {
	if user == nil {
		return nil
	}
	arr, _ := user["user_details"].([]any)
	if len(arr) == 0 {
		return nil
	}
	first, _ := arr[0].(map[string]any)
	return first
}

func userInfoFromDetails(details map[string]any, fallbackEmail string) UserInfo {
	email, _ := details["auth0Email"].(string)
	email = strings.TrimSpace(email)
	if email == "" {
		email = fallbackEmail
	}
	asInt := func(key string) int64 {
		switch v := details[key].(type) {
		case float64:
			return int64(v)
		case int:
			return int64(v)
		case int64:
			return v
		}
		return 0
	}
	return UserInfo{
		Email:  email,
		Tokens: asInt("subscriptionTokens") + asInt("paidTokens") + asInt("rolloverTokens"),
	}
}

// UploadImageURL downloads a remote image, then re-uploads it to Leonardo.
func (c *Client) UploadImageURL(token, imageURL string) (string, error) {
	body, contentType, err := c.Download(imageURL)
	if err != nil {
		return "", err
	}
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(stripQuery(imageURL)), "."))
	if ext != "jpg" && ext != "jpeg" && ext != "png" && ext != "webp" {
		ext = guessExtFromMime(contentType)
	}
	return c.uploadImageBytes(token, body, ext)
}

func stripQuery(rawURL string) string {
	if i := strings.IndexByte(rawURL, '?'); i >= 0 {
		return rawURL[:i]
	}
	return rawURL
}

func guessExtFromMime(mime string) string {
	switch strings.ToLower(strings.SplitN(mime, ";", 2)[0]) {
	case "image/png":
		return "png"
	case "image/webp":
		return "webp"
	default:
		return "jpg"
	}
}

// UploadImageBytes is a public wrapper around the internal upload helper.
// Used by the desktop UI to push raw file bytes (drag-drop / file picker)
// up to Leonardo without requiring a public URL.
func (c *Client) UploadImageBytes(token string, content []byte, ext string) (string, error) {
	return c.uploadImageBytes(token, content, ext)
}

func (c *Client) uploadImageBytes(token string, content []byte, ext string) (string, error) {
	if ext == "" {
		ext = "jpg"
	}
	gqlExt := ext
	if ext == "jpeg" {
		gqlExt = "jpg"
	}
	contentType := map[string]string{
		"jpg":  "image/jpeg",
		"jpeg": "image/jpeg",
		"png":  "image/png",
		"webp": "image/webp",
	}[ext]
	if contentType == "" {
		contentType = "image/jpeg"
	}

	log.Printf("[leonardo.upload] step1 UploadImage mutation: ext=%s gqlExt=%s contentType=%s", ext, gqlExt, contentType)

	uploadOp := gqlPayload{
		OperationName: "UploadImage",
		Variables: map[string]any{
			"uploadImageInput": map[string]any{
				"uploadType": "INIT",
				"extension":  gqlExt,
			},
		},
		Query: `mutation UploadImage($uploadImageInput: UploadImageInput!) {
  uploadImage(arg1: $uploadImageInput) {
    uploadId url fields __typename
  }
}`,
	}

	resp, err := c.gql(token, uploadOp)
	if err != nil {
		log.Printf("[leonardo.upload] mutation gql error: %v", err)
		return "", err
	}
	if msg := GraphQLErrorMessage(resp); msg != "" {
		log.Printf("[leonardo.upload] mutation graphql error: %s", msg)
		return "", fmt.Errorf("leonardo: UploadImage: %s", msg)
	}
	data, _ := resp["data"].(map[string]any)
	upload, _ := data["uploadImage"].(map[string]any)
	if upload == nil {
		log.Printf("[leonardo.upload] empty uploadImage payload: %+v", resp)
		return "", fmt.Errorf("leonardo: UploadImage mutation failed (empty payload)")
	}
	uploadID, _ := upload["uploadId"].(string)
	s3URL, _ := upload["url"].(string)
	rawFields, _ := upload["fields"].(string)

	log.Printf("[leonardo.upload] step2 got presigned url, uploadId=%s s3=%s", uploadID, s3URL)

	var fields map[string]string
	if err := json.Unmarshal([]byte(rawFields), &fields); err != nil {
		log.Printf("[leonardo.upload] parse fields failed: %v rawFields=%s", err, rawFields)
		return "", fmt.Errorf("leonardo: parse upload fields: %w", err)
	}

	boundary := fmt.Sprintf("----LeoUpload%d", time.Now().UnixMilli())
	var body bytes.Buffer
	for k, v := range fields {
		fmt.Fprintf(&body, "--%s\r\n", boundary)
		fmt.Fprintf(&body, "Content-Disposition: form-data; name=\"%s\"\r\n\r\n%s\r\n", k, v)
	}
	fileName := fmt.Sprintf("upload.%s", ext)
	fmt.Fprintf(&body, "--%s\r\n", boundary)
	fmt.Fprintf(&body, "Content-Disposition: form-data; name=\"file\"; filename=\"%s\"\r\n", fileName)
	fmt.Fprintf(&body, "Content-Type: %s\r\n\r\n", contentType)
	body.Write(content)
	fmt.Fprintf(&body, "\r\n--%s--\r\n", boundary)

	log.Printf("[leonardo.upload] step3 POST to s3: bodySize=%d", body.Len())

	respUpload, err := c.httpClient.R().
		SetHeader("Content-Type", "multipart/form-data; boundary="+boundary).
		SetBody(body.Bytes()).
		Post(s3URL)
	if err != nil {
		log.Printf("[leonardo.upload] s3 upload error: %v", err)
		return "", fmt.Errorf("leonardo: s3 upload: %w", err)
	}
	defer respUpload.Body.Close()
	if respUpload.StatusCode != 200 && respUpload.StatusCode != 204 {
		body, _ := io.ReadAll(respUpload.Body)
		snippet := string(body)
		if len(snippet) > 300 {
			snippet = snippet[:300]
		}
		log.Printf("[leonardo.upload] s3 status=%d body=%s", respUpload.StatusCode, snippet)
		return "", fmt.Errorf("leonardo: s3 upload status %d: %s", respUpload.StatusCode, snippet)
	}

	log.Printf("[leonardo.upload] step4 polling moderation for uploadId=%s", uploadID)

	moderationOp := gqlPayload{
		OperationName: "GetInitImageModeration",
		Variables:     map[string]any{"akUUID": uploadID},
		Query: `query GetInitImageModeration($akUUID: uuid!) {
  init_image_moderation(where: {akUUID: {_eq: $akUUID}}) {
    akUUID initImageId checkStatus __typename
  }
}`,
	}

	for i := 0; i < 30; i++ {
		time.Sleep(2 * time.Second)
		modResp, err := c.gql(token, moderationOp)
		if err != nil {
			log.Printf("[leonardo.upload] poll #%d gql error: %v", i, err)
			continue
		}
		data, _ := modResp["data"].(map[string]any)
		records, _ := data["init_image_moderation"].([]any)
		if len(records) == 0 {
			continue
		}
		record, _ := records[0].(map[string]any)
		status, _ := record["checkStatus"].(string)
		initID, _ := record["initImageId"].(string)
		log.Printf("[leonardo.upload] poll #%d status=%s initID=%s", i, status, initID)
		switch status {
		case "Accepted":
			if initID != "" {
				return initID, nil
			}
		case "Rejected":
			return "", fmt.Errorf("leonardo: image rejected by moderation")
		}
	}
	return "", fmt.Errorf("leonardo: moderation timeout")
}


// modelSlugMap maps display names to Leonardo GraphQL model slugs.
// The request.model field in the GraphQL mutation requires a slug, not a UUID.
var modelSlugMap = map[string]string{
	"nano-banana-2":       "nano-banana-2",
	"nano banana 2":       "nano-banana-2",
	"nano-banana-pro":     "nano-banana-pro",
	"nano banana pro":     "nano-banana-pro",
	"gpt-image-2":         "gpt-image-2",
	"gpt image-2":         "gpt-image-2",
	"gpt image 2":         "gpt-image-2",
	"gpt-image-1.5":       "gpt-image-1.5",
	"gpt image-1.5":       "gpt-image-1.5",
	"gpt image 1.5":       "gpt-image-1.5",
	"gpt-image-1":         "gpt-image-1",
	"gpt image-1":         "gpt-image-1",
	"gpt image 1":         "gpt-image-1",
	"lucid-origin":        "lucid-origin",
	"lucid origin":        "lucid-origin",
	"lucid-realism":       "lucid-realism",
	"lucid realism":       "lucid-realism",
	"flux-pro-2.0":        "flux-pro-2.0",
	"flux.2 pro":          "flux-pro-2.0",
	"flux-pro-2":          "flux-pro-2.0",
	"flux-kontext-pro":    "flux-kontext-pro",
	"flux.1 kontext":      "flux-kontext-pro",
	"flux.1 kontext max":  "flux-kontext-pro",
	"flux-max":            "flux-kontext-pro",
	"flux-dev":            "flux-dev",
	"flux dev":            "flux-dev",
	"flux-dev-2.0":        "flux-dev-2.0",
	"flux.2 dev":          "flux-dev-2.0",
	"flux-schnell":        "flux-schnell",
	"flux schnell":        "flux-schnell",
	"flux-omni":           "flux-kontext-pro",
	"seedream-4.5":        "seedream-4.5",
	"seedream 4.5":        "seedream-4.5",
	"seedream-4.0":        "seedream-4.0",
	"seedream 4":          "seedream-4.0",
	"seedream 4.0":        "seedream-4.0",
	"phoenix-1.0":         "phoenix-1.0",
	"phoenix 1.0":         "phoenix-1.0",
	"phoenix-0.9":         "phoenix-0.9",
	"phoenix 0.9":         "phoenix-0.9",
	"phoenix":             "phoenix-1.0",
	"ideogram-v4.0":       "ideogram-v4.0",
	"ideogram 4.0":        "ideogram-v4.0",
	"ideogram-3":          "ideogram-3.0",
	"ideogram 3.0":        "ideogram-3.0",
	"recraft-v4":          "recraft-v4",
	"recraft v4":          "recraft-v4",
	"recraft-v4-pro":      "recraft-v4-pro",
	"recraft v4 pro":      "recraft-v4-pro",
	"recraft_v4_pro":      "recraft-v4-pro",
	"kino-2-1":            "lucid-origin",
	"kino_2_1":            "lucid-origin",
	"kino-2-0":            "lucid-realism",
	"kino_2_0":            "lucid-realism",
	"gemini-image-2":      "nano-banana-pro",
	"gemini_image_2":      "nano-banana-pro",
	"gemini-2-5-flash":    "nano-banana",
	"gemini_2_5_flash":    "nano-banana",
	"nano-banana":         "nano-banana",
	"nano banana":         "nano-banana",
}

// defaultStyleID is "Dynamic" — used when no style is specified.
const defaultStyleID = "111dc692-d470-4eec-b791-3475abac4c46"

// styleNameToUUID maps human-readable style names to Leonardo UUIDs.
var styleNameToUUID = map[string]string{
	"none":         "556c1ee5-ec38-42e8-955a-1e82dad0ffa1",
	"cinematic":    "a5632c7c-ddbb-4e2f-ba34-8456ab3ac436",
	"creative":     "6fedbf1f-4a17-45ec-84fb-92fe524a29ef",
	"dynamic":      "111dc692-d470-4eec-b791-3475abac4c46",
	"fashion":      "594c4a08-a522-4e0e-b7ff-e4dac4b6b622",
	"portrait":     "ab5a4220-7c42-41e5-a578-eddb9fed3d75",
	"stock photo":  "5bdc3f2a-1be6-4d1c-8e77-992a30824a2c",
	"stock-photo":  "5bdc3f2a-1be6-4d1c-8e77-992a30824a2c",
	"vibrant":      "dee282d3-891f-4f73-ba02-7f8131e5541b",
}

// resolveStyleID converts a style name or UUID to a Leonardo style UUID.
// Empty input returns the default (Dynamic).
func resolveStyleID(style string) string {
	style = strings.ToLower(strings.TrimSpace(style))
	if style == "" {
		return defaultStyleID
	}
	if id, ok := styleNameToUUID[style]; ok {
		return id
	}
	// Assume it's already a UUID
	return style
}

// resolveModelSlug converts a display name, UUID, or SDVersion to a Leonardo GraphQL slug.
// Falls back to "nano-banana-2" if no mapping found.
func resolveModelSlug(modelID, sdVersion string) string {
	// If it's already a slug (contains no spaces, lowercase with hyphens), use as-is
	if strings.Contains(modelID, "-") && !strings.Contains(modelID, " ") {
		return modelID
	}
	// Try display name lookup (case-insensitive)
	lower := strings.ToLower(strings.TrimSpace(modelID))
	if slug, ok := modelSlugMap[lower]; ok {
		return slug
	}
	// Try SDVersion-based fallback
	sdUpper := strings.ToUpper(strings.TrimSpace(sdVersion))
	switch sdUpper {
	case "GPT_IMAGE_2":
		return "gpt-image-2"
	case "GPT_IMAGE_1":
		return "gpt-image-1"
	case "IDEOGRAM_4":
		return "ideogram-v4.0"
	case "IDEOGRAM_3":
		return "ideogram-3.0"
	case "RECRAFT_V4_PRO":
		return "recraft-v4-pro"
	case "RECRAFT_V4":
		return "recraft-v4"
	case "NANO_BANANA_2":
		return "nano-banana-2"
	case "NANO_BANANA_PRO", "GEMINI_IMAGE_2":
		return "nano-banana-pro"
	case "GEMINI_2_5_FLASH", "NANO_BANANA":
		return "nano-banana"
	case "SEEDREAM_4_5":
		return "seedream-4.5"
	case "SEEDREAM_4_0":
		return "seedream-4.0"
	case "FLUX_PRO_2_0":
		return "flux-pro-2.0"
	case "FLUX_DEV_2_0":
		return "flux-dev-2.0"
	case "FLUX_DEV":
		return "flux-dev"
	case "FLUX":
		return "flux-schnell"
	case "FLUX_OMNI", "FLUX_MAX":
		return "flux-kontext-pro"
	case "PHOENIX":
		return "phoenix-1.0"
	case "KINO_2_1":
		return "lucid-origin"
	case "KINO_2_0":
		return "lucid-realism"
	case "SDXL_LIGHTNING", "SDXL_0_9", "SDXL_1_0":
		return "phoenix-0.9"
	}
	return "nano-banana-2" // safe default
}

// CreateGeneration submits a Generate mutation and returns the generation ID.
func (c *Client) CreateGeneration(token string, in GenerateInput) (string, error) {
	params := map[string]any{
		"width":               in.Width,
		"height":              in.Height,
		"prompt":              strings.TrimSpace(in.Prompt),
		"quantity":            in.Quantity,
		"style_ids":           []string{resolveStyleID(in.StyleID)},
		"prompt_enhance":      "ON",
		"dimensions":          fmt.Sprintf("%dx%d", in.Width, in.Height),
		"modelId":             in.ModelID,
		"negative_prompt":     "",
		"guidance_scale":      7.0,
		"num_inference_steps": 30,
	}

	sd := strings.ToUpper(strings.TrimSpace(in.SDVersion))
	if sd == "GPT_IMAGE_2" || sd == "GPT_IMAGE_1" {
		params["quality"] = "low"
		delete(params, "guidance_scale")
		delete(params, "num_inference_steps")
		delete(params, "prompt_enhance")
	}
	// Models that don't accept style/size params (per Hermes verification).
	if sd == "IDEOGRAM_4" || sd == "RECRAFT_V4" || sd == "RECRAFT_V4_PRO" {
		delete(params, "style_ids")
		delete(params, "dimensions")
	}

	if len(in.InitImageIDs) > 0 {
		references := make([]map[string]any, 0, len(in.InitImageIDs))
		for _, id := range in.InitImageIDs {
			references = append(references, map[string]any{
				"image":    map[string]any{"id": id, "type": "UPLOADED"},
				"strength": "MID",
			})
		}
		params["guidances"] = map[string]any{"image_reference": references}
	}

	op := gqlPayload{
		OperationName: "Generate",
		Variables: map[string]any{
			"request": map[string]any{
				"model":      "nano-banana-2",
				"parameters": params,
				"public":     true,
			},
		},
		Query: `mutation Generate($request: CreateGenerationRequest!) {
  generate(request: $request) {
    apiCreditCost generationId __typename
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
	return "", fmt.Errorf("leonardo: generate failed")
}

// PollStatus returns COMPLETED/PENDING/FAILED/etc.
func (c *Client) PollStatus(token, genID string) (string, error) {
	op := gqlPayload{
		OperationName: "GetAIGenerationFeedStatuses",
		Variables: map[string]any{
			"where": map[string]any{
				"id": map[string]any{"_eq": genID},
			},
		},
		Query: `query GetAIGenerationFeedStatuses($where: generations_bool_exp = {}) {
  generations(where: $where) {
    id status __typename
  }
}`,
	}
	resp, err := c.gql(token, op)
	if err != nil {
		return "", err
	}
	data, _ := resp["data"].(map[string]any)
	gens, _ := data["generations"].([]any)
	if len(gens) == 0 {
		return "PENDING", nil
	}
	first, _ := gens[0].(map[string]any)
	status, _ := first["status"].(string)
	if status == "" {
		status = "PENDING"
	}
	return status, nil
}

// GetImageURLs fetches the generated image URLs once a job is complete.
func (c *Client) GetImageURLs(token, genID string) ([]string, error) {
	op := gqlPayload{
		OperationName: "GetAIGenerationFeed",
		Variables: map[string]any{
			"where": map[string]any{"id": map[string]any{"_eq": genID}},
			"limit": 1,
		},
		Query: `query GetAIGenerationFeed($where: generations_bool_exp = {}, $limit: Int) {
  generations(where: $where, limit: $limit) {
    generated_images(order_by: [{url: desc}]) {
      url id __typename
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
	out := make([]string, 0, len(images))
	for _, item := range images {
		obj, _ := item.(map[string]any)
		if u, ok := obj["url"].(string); ok && u != "" {
			out = append(out, u)
		}
	}
	return out, nil
}

// isAuthErrorPoll detects auth failures during polling (JWT expired, 401, etc).
func isAuthErrorPoll(msg string) bool {
	lower := strings.ToLower(strings.TrimSpace(msg))
	return strings.Contains(lower, "jwt expired") ||
		strings.Contains(lower, "token expired") ||
		strings.Contains(lower, "unauthorized") ||
		strings.Contains(lower, "401") ||
		strings.Contains(lower, "invalid token") ||
		strings.Contains(lower, "invalid bearer")
}

// WaitForCompletion polls until the generation finishes, errors, or times out.
// If tokenRefresh is non-nil and a poll fails with an auth error, it is called
// to obtain a fresh token before the next attempt.
func (c *Client) WaitForCompletion(token, genID string, timeout, pollInterval time.Duration, tokenRefresh func() string) GenerationResult {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		status, err := c.PollStatus(token, genID)
		if err == nil {
			switch status {
			case "COMPLETED":
				images, _ := c.GetImageURLs(token, genID)
				return GenerationResult{Success: true, Images: images}
			case "FAILED", "ERROR":
				return GenerationResult{Success: false, Error: "generation failed"}
			}
		} else if tokenRefresh != nil && isAuthErrorPoll(err.Error()) {
			if fresh := tokenRefresh(); fresh != "" {
				token = fresh
			}
		}
		time.Sleep(pollInterval)
	}

	if images, err := c.GetImageURLs(token, genID); err == nil && len(images) > 0 {
		return GenerationResult{Success: true, Images: images}
	}
	return GenerationResult{Success: false, Error: "generation timeout"}
}

// IsLikelyLeonardoTokenString is a thin alias for code that holds Client.
func (c *Client) IsLikelyLeonardoTokenString(token string) bool {
	return IsLikelyLeonardoToken(token)
}

// helper used by service to avoid importing encoding/base64 elsewhere.
var _ = base64.StdEncoding

// ensure io is referenced.
var _ = io.Discard

// CustomModelEntry is one row from the Leonardo `custom_models` table.
type CustomModelEntry struct {
	ID        string
	Name      string
	SDVersion string
	Type      string
}

// FetchOfficialImageModels returns Leonardo's official GENERATE-type image
// models via the custom_models GraphQL endpoint.
func (c *Client) FetchOfficialImageModels(token string) ([]CustomModelEntry, error) {
	resp, err := c.gql(token, gqlPayload{
		OperationName: "GetFeedModels",
		Variables: map[string]any{
			"where": map[string]any{
				"public": map[string]any{"_eq": true},
				"status": map[string]any{"_eq": "COMPLETE"},
				"type":   map[string]any{"_eq": "GENERATE"},
			},
			"limit":  200,
			"offset": 0,
		},
		Query: `query GetFeedModels($where: custom_models_bool_exp = {}, $limit: Int, $offset: Int) {
  custom_models(where: $where, limit: $limit, offset: $offset, order_by: [{createdAt: desc}]) {
    id name type sdVersion public status __typename
  }
}`,
	})
	if err != nil {
		return nil, fmt.Errorf("leonardo: fetch official models: %w", err)
	}

	data, _ := resp["data"].(map[string]any)
	raw, _ := data["custom_models"].([]any)
	if len(raw) == 0 {
		if msg := GraphQLErrorMessage(resp); msg != "" {
			return nil, fmt.Errorf("leonardo: fetch official models: %s", msg)
		}
		return nil, fmt.Errorf("leonardo: fetch official models: no models returned")
	}

	out := make([]CustomModelEntry, 0, len(raw))
	for _, item := range raw {
		obj, _ := item.(map[string]any)
		if obj == nil {
			continue
		}
		id, _ := obj["id"].(string)
		if id == "" {
			continue
		}
		name, _ := obj["name"].(string)
		sd, _ := obj["sdVersion"].(string)
		mtype, _ := obj["type"].(string)
		out = append(out, CustomModelEntry{
			ID:        id,
			Name:      name,
			SDVersion: sd,
			Type:      mtype,
		})
	}
	return out, nil
}
