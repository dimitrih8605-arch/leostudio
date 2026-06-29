import { useCallback, useEffect, useState } from "react";
import {
  ImageIcon,
  Sparkles,
  Wand2,
  Loader2,
  Download,
} from "lucide-react";
import { Card, CardContent, CardDescription, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { Select } from "@/components/ui/select";
import { Slider } from "@/components/ui/slider";
import { Skeleton } from "@/components/ui/skeleton";
import { Badge } from "@/components/ui/badge";
import { Lightbox } from "@/components/ui/lightbox";
import { ImageDropzone, type DroppedImage } from "@/components/image-dropzone";
import { useToast } from "@/components/ui/toast";
import {
  api,
  type AspectRatioOption,
  type ImageGenerateResponse,
  type ImageModel,
} from "@/lib/api";
import { consumeReplay, onReplay } from "@/lib/replay";

export function GenerateImagePage() {
  const { showSuccess, showError } = useToast();

  const [models, setModels] = useState<ImageModel[] | null>(null);
  const [aspects, setAspects] = useState<AspectRatioOption[] | null>(null);

  const [prompt, setPrompt] = useState("");
  const [modelId, setModelId] = useState<string>("");
  const [aspect, setAspect] = useState<string>("1:1");
  const [n, setN] = useState(1);
  const [style, setStyle] = useState<string>("Dynamic");
  const [refs, setRefs] = useState<DroppedImage[]>([]);

  const [generating, setGenerating] = useState(false);
  const [result, setResult] = useState<ImageGenerateResponse | null>(null);
  const [preview, setPreview] = useState<string | null>(null);

  const loadConfig = useCallback(async () => {
    try {
      const [modelList, aspectList, defAspect] = await Promise.all([
        api.listImageModels(),
        api.listImageAspects(),
        api.getSetting("default_aspect_ratio", "1:1"),
      ]);
      setModels(modelList);
      setAspects(aspectList);
      setAspect(defAspect || "1:1");
      // Pre-select default model.
      const def = modelList.find((m) => m.isDefault);
      if (def) {
        setModelId(def.modelId);
      } else if (modelList.length > 0) {
        setModelId(modelList[0].modelId);
      }
    } catch (err) {
      showError(`Gagal load konfigurasi: ${(err as Error).message}`);
    }
  }, [showError]);

  useEffect(() => {
    void loadConfig();
  }, [loadConfig]);

  // Consume replay payload on mount and whenever Library re-emits.
  useEffect(() => {
    const apply = () => {
      const payload = consumeReplay("image");
      if (!payload) return;
      setPrompt(payload.prompt);
      if (payload.aspectRatio) setAspect(payload.aspectRatio);
      if (payload.modelId) setModelId(payload.modelId);
    };
    apply();
    return onReplay(apply);
  }, []);

  const onGenerate = async () => {
    const p = prompt.trim();
    if (!p) {
      showError("Prompt tidak boleh kosong.");
      return;
    }
    setGenerating(true);
    setResult(null);
    try {
      const res = await api.generateImage({
        prompt: p,
        modelId: modelId || undefined,
        n,
        aspectRatio: aspect,
        style: style === "Dynamic" ? undefined : style,
        referenceImageURLs: refs
          .filter((r) => r.source === "url" && r.url)
          .map((r) => r.url as string),
        referenceImageIds: refs
          .filter((r) => r.source === "uploaded" && r.imageId)
          .map((r) => r.imageId as string),
      });
      setResult(res);

      // Surface auto-save outcome so the user knows where (or why not) the
      // file was saved without diving into Settings.
      if (res.provider.save_error) {
        showError(`Auto-save gagal: ${res.provider.save_error}`);
      } else if (
        res.provider.auto_save_enabled &&
        res.provider.saved_files.length > 0
      ) {
        showSuccess(
          `Saved ${res.provider.saved_files.length} file → ${res.provider.saved_files[0]}`
        );
      } else {
        showSuccess(`Generate sukses · ${res.data.length} image`);
      }
    } catch (err) {
      showError((err as Error).message);
    } finally {
      setGenerating(false);
    }
  };

  if (models === null || aspects === null) {
    return <GenerateImageSkeleton />;
  }

  return (
    <div className="grid h-full grid-cols-1 gap-6 overflow-hidden p-6 lg:grid-cols-[420px_1fr]">
      <Card className="self-start lg:max-h-full lg:overflow-y-auto">
        <div className="flex items-center gap-2 p-5 pb-3">
          <Wand2 className="h-4 w-4 text-primary" />
          <CardTitle className="text-base">Compose</CardTitle>
        </div>
        <CardContent className="space-y-4 pt-0">
          <Field label="Prompt">
            <Textarea
              value={prompt}
              onChange={(e) => setPrompt(e.target.value)}
              onKeyDown={(e) => {
                if ((e.ctrlKey || e.metaKey) && e.key === "Enter") {
                  e.preventDefault();
                  if (!generating && prompt.trim() && modelId) void onGenerate();
                }
              }}
              placeholder="A cinematic shot of..."
              className="min-h-[120px]"
              spellCheck={false}
            />
          </Field>
          <div className="grid grid-cols-2 gap-3">
            <Field label="Model">
              <Select
                value={modelId}
                onChange={(e) => setModelId(e.target.value)}
              >
                {models.length === 0 && <option value="">No model</option>}
                {models.map((m) => (
                  <option key={m.modelId} value={m.modelId}>
                    {m.name}
                    {m.isDefault ? " · default" : ""}
                  </option>
                ))}
              </Select>
            </Field>
            <Field label="Aspect ratio">
              <Select
                value={aspect}
                onChange={(e) => setAspect(e.target.value)}
              >
                {aspects.map((a) => (
                  <option key={a.label} value={a.label}>
                    {a.label}
                  </option>
                ))}
              </Select>
            </Field>
          </div>

          {(() => {
            const m = models.find((m) => m.modelId === modelId);
            const hideStyle = m && (m.sdVersion === "IDEOGRAM_4" || m.sdVersion === "RECRAFT_V4");
            if (hideStyle) return null;
            return (
              <Field label="Style">
                <Select value={style} onChange={(e) => setStyle(e.target.value)}>
                  <option value="Dynamic">Dynamic</option>
                  <option value="Cinematic">Cinematic</option>
                  <option value="Creative">Creative</option>
                  <option value="Fashion">Fashion</option>
                  <option value="Portrait">Portrait</option>
                  <option value="Stock Photo">Stock Photo</option>
                  <option value="Vibrant">Vibrant</option>
                  <option value="None">None</option>
                </Select>
              </Field>
            );
          })()}

          <Field label={`Quantity · ${n}`}>
            <Slider
              value={n}
              onValueChange={setN}
              min={1}
              max={4}
              step={1}
            />
          </Field>

          <div className="space-y-2">
            <p className="text-xs font-medium text-muted-foreground">
              Reference <span className="text-[10px]">({refs.length}/3)</span>
            </p>
            <div className="space-y-2">
              {refs.map((r, i) => (
                <ImageDropzone
                  key={i}
                  value={r}
                  onChange={(next) => {
                    setRefs((prev) => {
                      if (next === null) {
                        return prev.filter((_, idx) => idx !== i);
                      }
                      const copy = [...prev];
                      copy[i] = next;
                      return copy;
                    });
                  }}
                />
              ))}
              {refs.length < 3 && (
                <ImageDropzone
                  value={null}
                  onChange={(next) => {
                    if (next) setRefs([...refs, next]);
                  }}
                />
              )}
            </div>
          </div>

          <Button
            className="w-full"
            size="lg"
            onClick={onGenerate}
            disabled={generating || !prompt.trim() || !modelId}
          >
            {generating ? (
              <>
                <Loader2 className="h-4 w-4 animate-spin" />
                Generating...
              </>
            ) : (
              <>
                <Sparkles className="h-4 w-4" />
                Generate
                <span className="ml-1 hidden text-[10px] opacity-60 sm:inline">
                  Ctrl+Enter
                </span>
              </>
            )}
          </Button>
        </CardContent>
      </Card>

      <ResultArea
        generating={generating}
        result={result}
        aspect={aspect}
        n={n}
        onPreview={(url) => setPreview(url)}
      />
      <Lightbox url={preview} onClose={() => setPreview(null)} />
    </div>
  );
}

// Map aspect ratio string to a Tailwind aspect-* class so loading skeletons
// and finished tiles share the exact same dimensions.
function aspectClass(aspect: string): string {
  if (aspect === "9:16") return "aspect-[9/16]";
  if (aspect === "16:9") return "aspect-[16/9]";
  if (aspect === "4:3") return "aspect-[4/3]";
  return "aspect-square";
}

function ResultArea({
  generating,
  result,
  aspect,
  n,
  onPreview,
}: {
  generating: boolean;
  result: ImageGenerateResponse | null;
  aspect: string;
  n: number;
  onPreview: (url: string) => void;
}) {
  return (
    <Card className="flex min-h-[420px] flex-col lg:max-h-full">
      <div className="flex items-center justify-between border-b border-border/60 p-5 pb-3">
        <div>
          <CardTitle className="text-base">Result</CardTitle>
          <CardDescription>
            {result
              ? `${result.data.length} image · cookie #${result.provider.used_cookie_id}`
              : "Generated images appear here."}
          </CardDescription>
        </div>
        {result?.provider.generation_id ? (
          <Badge tone="info">{result.provider.generation_id.slice(0, 8)}</Badge>
        ) : null}
      </div>
      <CardContent className="flex-1 overflow-y-auto p-5 pt-4">
        {generating ? (
          <ResultSkeletons aspect={aspect} count={n} />
        ) : result ? (
          <div
            className={
              result.data.length > 1
                ? "grid grid-cols-2 gap-3"
                : "flex justify-center"
            }
          >
            {result.data.map((item, i) => (
              <ImageTile
                key={`${item.url}-${i}`}
                url={item.url}
                aspect={aspect}
                solo={result.data.length === 1}
                onPreview={() => onPreview(item.url)}
              />
            ))}
          </div>
        ) : (
          <EmptyState />
        )}
      </CardContent>
    </Card>
  );
}

function ImageTile({
  url,
  aspect,
  solo,
  onPreview,
}: {
  url: string;
  aspect: string;
  solo: boolean;
  onPreview: () => void;
}) {
  return (
    <button
      type="button"
      onClick={onPreview}
      className={`group relative block overflow-hidden rounded-lg border border-border bg-background/30 text-left ${
        solo ? "w-full max-w-3xl" : "w-full"
      }`}
      aria-label="Preview image"
    >
      <img
        src={url}
        alt="generated"
        className={`w-full object-cover transition group-hover:scale-[1.02] ${aspectClass(aspect)}`}
        loading="lazy"
      />
      <span className="absolute right-2 top-2 inline-flex items-center gap-1 rounded-md bg-background/80 px-2 py-1 text-[10px] backdrop-blur transition group-hover:opacity-100">
        <Download className="h-3 w-3" />
        Preview
      </span>
    </button>
  );
}

function ResultSkeletons({ aspect, count }: { aspect: string; count: number }) {
  const ratio = aspectClass(aspect);
  // Single image fills the panel; only switch to 2 columns when generating
  // multiple so each tile stays large and readable.
  if (count <= 1) {
    return (
      <div className="flex justify-center">
        <Skeleton className={`w-full max-w-3xl ${ratio} rounded-lg`} />
      </div>
    );
  }
  return (
    <div className="grid grid-cols-2 gap-3">
      {Array.from({ length: count }).map((_, i) => (
        <Skeleton key={i} className={`w-full ${ratio} rounded-lg`} />
      ))}
    </div>
  );
}

function EmptyState() {
  return (
    <div className="flex flex-col items-center justify-center gap-2 py-16 text-center">
      <div className="flex h-12 w-12 items-center justify-center rounded-xl bg-accent text-accent-foreground">
        <ImageIcon className="h-5 w-5" />
      </div>
      <p className="text-sm font-medium">Ready to generate</p>
    </div>
  );
}

function Field({
  label,
  hint,
  children,
}: {
  label: string;
  hint?: string;
  children: React.ReactNode;
}) {
  return (
    <div className="space-y-1.5">
      <p className="text-xs font-medium text-muted-foreground">{label}</p>
      {children}
      {hint ? <p className="text-[10px] text-muted-foreground/70">{hint}</p> : null}
    </div>
  );
}

function GenerateImageSkeleton() {
  return (
    <div className="grid grid-cols-1 gap-6 p-6 lg:grid-cols-[420px_1fr]">
      <Card>
        <div className="space-y-4 p-5">
          <Skeleton className="h-5 w-24" />
          <Skeleton className="h-28 w-full" />
          <div className="grid grid-cols-2 gap-3">
            <Skeleton className="h-9 w-full" />
            <Skeleton className="h-9 w-full" />
          </div>
          <Skeleton className="h-9 w-full" />
          <Skeleton className="h-10 w-full" />
        </div>
      </Card>
      <Card>
        <div className="p-5">
          <Skeleton className="h-72 w-full" />
        </div>
      </Card>
    </div>
  );
}
