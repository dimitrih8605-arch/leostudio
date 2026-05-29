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

// CreateGeneration submits a Generate mutation and returns the generation ID.
func (c *Client) CreateGeneration(token string, in GenerateInput) (string, error) {
	params := map[string]any{
		"width":               in.Width,
		"height":              in.Height,
		"prompt":              strings.TrimSpace(in.Prompt),
		"quantity":            in.Quantity,
		"style_ids":           []string{"111dc692-d470-4eec-b791-3475abac4c46"},
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
		delete(params, "style_ids")
		delete(params, "guidance_scale")
		delete(params, "num_inference_steps")
		delete(params, "prompt_enhance")
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

// WaitForCompletion polls until the generation finishes, errors, or times out.
func (c *Client) WaitForCompletion(token, genID string, timeout, pollInterval time.Duration) GenerationResult {
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

// FetchOfficialImageModels returns Leonardo's official (username = "Leonardo")
// image models. We try GraphQL queries that have historically been valid;
// the first one that returns a non-empty list wins.
func (c *Client) FetchOfficialImageModels(token string) ([]CustomModelEntry, error) {
	queries := []gqlPayload{
		{
			OperationName: "GetFeedModels",
			Variables: map[string]any{
				"where": map[string]any{
					"public": map[string]any{"_eq": true},
					"status": map[string]any{"_eq": "COMPLETE"},
				},
				"limit":  200,
				"offset": 0,
			},
			Query: `query GetFeedModels($where: custom_models_bool_exp = {}, $limit: Int, $offset: Int) {
  custom_models(where: $where, limit: $limit, offset: $offset, order_by: [{createdAt: desc}]) {
    id name type sdVersion public status
    user { username __typename }
    __typename
  }
}`,
		},
		{
			OperationName: "GetFeedModelsMinimal",
			Variables: map[string]any{
				"where": map[string]any{
					"public": map[string]any{"_eq": true},
					"status": map[string]any{"_eq": "COMPLETE"},
				},
				"limit":  200,
				"offset": 0,
			},
			Query: `query GetFeedModelsMinimal($where: custom_models_bool_exp = {}, $limit: Int, $offset: Int) {
  custom_models(where: $where, limit: $limit, offset: $offset, order_by: [{createdAt: desc}]) {
    id name type sdVersion __typename
  }
}`,
		},
	}

	var lastError string
	for _, op := range queries {
		resp, err := c.gql(token, op)
		if err != nil {
			lastError = err.Error()
			continue
		}
		data, _ := resp["data"].(map[string]any)
		raw, _ := data["custom_models"].([]any)
		if len(raw) == 0 {
			if msg := GraphQLErrorMessage(resp); msg != "" {
				lastError = msg
			}
			continue
		}

		out := make([]CustomModelEntry, 0, len(raw))
		for _, item := range raw {
			obj, _ := item.(map[string]any)
			if obj == nil {
				continue
			}
			// Filter to official Leonardo models only. Unknown user shape
			// (minimal query) is treated as official to avoid losing rows.
			if userObj, ok := obj["user"].(map[string]any); ok {
				if u, _ := userObj["username"].(string); u != "" && u != "Leonardo" {
					continue
				}
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
		if len(out) > 0 {
			return out, nil
		}
	}

	if lastError == "" {
		lastError = "no models returned by Leonardo"
	}
	return nil, fmt.Errorf("leonardo: fetch official models: %s", lastError)
}
