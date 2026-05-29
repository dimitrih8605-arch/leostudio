// Thin typed wrapper around Wails-generated bindings. Wails injects
// window.go.<package>.<struct>.<method>. We avoid hard imports from
// wailsjs/go so the project compiles even before the first dev/build run.

export type Cookie = {
  id: number;
  email: string;
  is_active: boolean;
  last_balance: number;
  last_error: string;
  last_used_at: number;
  last_checked_at: number;
  disabled_reason: string;
  disabled_at: number;
  created_at: number;
  status: "READY" | "DEPLETED" | "DISABLED";
};

export type AddCookieResult = {
  email: string;
  balance: number;
};

export type CookieRefreshResult = {
  checked: number;
  ok: number;
};

export type CookieHealth = {
  total: number;
  ready: number;
  depleted: number;
  disabled: number;
  total_balance: number;
  active_balance: number;
};

export type ImageModel = {
  id: number;
  name: string;
  modelId: string;
  sdVersion: string;
  isDefault: boolean;
  createdAt: number;
};

export type VideoModel = {
  slug: string;
  defaultMode: string;
  supportedModes: string[];
  durationOptions: number[];
  defaultDuration: number;
  supportsAudio: boolean;
  supportsRefImage: boolean;
  defaultAspect: string;
};

export type AspectRatioOption = {
  label: string;
  width: number;
  height: number;
};

export type GenerationLog = {
  id: number;
  providerGenerationID: string;
  usedCookieID: number;
  modelID: string;
  aspectRatio: string;
  prompt: string;
  imageURLs: string[];
  savedFiles: string[];
  saveEnabled: boolean;
  status: string;
  errorMessage: string;
  createdAt: number;
};

export type ImageGenerateRequest = {
  prompt: string;
  modelId?: string;
  n?: number;
  aspectRatio?: string;
  referenceImageURLs?: string[];
  referenceImageIds?: string[];
};

export type ImageGenerateResponse = {
  created: number;
  data: Array<{ url: string }>;
  provider: {
    generation_id: string;
    used_cookie_id: number;
    aspect_ratio: string;
    model_id: string;
    saved_files: string[];
    auto_save_enabled: boolean;
    save_error?: string;
  };
};

export type VideoGenerateRequest = {
  prompt: string;
  modelSlug?: string;
  aspectRatio?: string;
  resolution?: string;
  duration?: number;
  audio?: boolean;
  imageURL?: string;
  imageId?: string;
};

export type VideoGenerateResponse = {
  created: number;
  data: Array<{
    url: string;
    mp4_url: string;
    gif_url?: string;
    thumbnail_url?: string;
    width?: number;
    height?: number;
  }>;
  provider: {
    generation_id: string;
    used_cookie_id: number;
    model: string;
    resolution: string;
    duration: number;
    aspect_ratio: string;
    audio: boolean;
    saved_files: string[];
    auto_save_enabled: boolean;
    save_error?: string;
  };
};

export type AppInfo = {
  name: string;
  version: string;
  author: string;
  repository: string;
  license: string;
};

export type ModelSyncResult = {
  Total: number;
  Added: number;
  Updated: number;
  Sample: string[];
};

function bindings() {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const w = window as any;
  if (!w.go || !w.go.desktop || !w.go.desktop.App) {
    throw new Error("Wails bindings not available yet (window.go.desktop.App)");
  }
  return w.go.desktop.App;
}

export const api = {
  ping: (): Promise<string> => bindings().Ping(),
  appInfo: (): Promise<AppInfo> => bindings().AppInfo(),
  openURL: (url: string): Promise<void> => bindings().OpenURL(url),

  // Cookies
  listCookies: (): Promise<Cookie[]> => bindings().ListCookies(),
  addCookie: (raw: string): Promise<AddCookieResult> =>
    bindings().AddCookie(raw),
  updateCookie: (id: number, raw: string): Promise<AddCookieResult> =>
    bindings().UpdateCookie(id, raw),
  deleteCookie: (id: number): Promise<void> => bindings().DeleteCookie(id),
  toggleCookie: (id: number, enabled: boolean): Promise<void> =>
    bindings().ToggleCookie(id, enabled),
  refreshCookieProfiles: (): Promise<CookieRefreshResult> =>
    bindings().RefreshCookieProfiles(),
  refreshCookieSessions: (): Promise<CookieRefreshResult> =>
    bindings().RefreshCookieSessions(),
  cookieHealth: (): Promise<CookieHealth> => bindings().CookieHealth(),

  // Settings
  getSetting: (key: string, fallback: string): Promise<string> =>
    bindings().GetSetting(key, fallback),
  setSetting: (key: string, value: string): Promise<void> =>
    bindings().SetSetting(key, value),

  // Image
  generateImage: (req: ImageGenerateRequest): Promise<ImageGenerateResponse> =>
    bindings().GenerateImage(req),
  listImageModels: (): Promise<ImageModel[]> => bindings().ListImageModels(),
  syncImageModels: (): Promise<ModelSyncResult> =>
    bindings().SyncImageModels(),
  addImageModel: (name: string, modelId: string): Promise<void> =>
    bindings().AddImageModel(name, modelId),
  deleteImageModel: (id: number): Promise<void> =>
    bindings().DeleteImageModel(id),
  setDefaultImageModel: (id: number): Promise<void> =>
    bindings().SetDefaultImageModel(id),
  listImageAspects: (): Promise<AspectRatioOption[]> =>
    bindings().ListImageAspects(),

  // Video
  generateVideo: (req: VideoGenerateRequest): Promise<VideoGenerateResponse> =>
    bindings().GenerateVideo(req),
  listVideoModels: (): Promise<VideoModel[]> => bindings().ListVideoModels(),

  // Library
  listGenerationLogs: (limit: number): Promise<GenerationLog[]> =>
    bindings().ListGenerationLogs(limit),

  // Filesystem dialogs
  openDirectoryDialog: (currentPath: string): Promise<string> =>
    bindings().OpenDirectoryDialog(currentPath),
  openInFileManager: (path: string): Promise<void> =>
    bindings().OpenInFileManager(path),
  downloadAsset: (url: string, suggestedName: string): Promise<string> =>
    bindings().DownloadAsset(url, suggestedName),

  // Local file upload (drag-drop / file picker)
  uploadLocalImage: (base64: string, extension: string): Promise<string> =>
    bindings().UploadLocalImage(base64, extension),
};
