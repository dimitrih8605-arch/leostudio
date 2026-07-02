#!/usr/bin/env python3
"""
LeoStudio MCP Server — wraps the LeoStudio REST API as MCP tools for Hermes.

Requires: leostudio-server running on 127.0.0.1:8000
Install:  source ~/HERMES_WORKSPACE/.venv/bin/activate && pip install mcp
Run:      python3 mcp_server.py  (stdio transport, used by Hermes)
"""

import json
import os
import sys
import httpx
from mcp.server.fastmcp import FastMCP

BASE_URL = os.environ.get("LEOSTUDIO_URL", "http://127.0.0.1:8000")
TIMEOUT = int(os.environ.get("LEOSTUDIO_TIMEOUT", "300"))  # generation can be slow

mcp = FastMCP(
    "leostudio",
    instructions=(
        "LeoStudio AI image and video generation. "
        "Control cookies, models, queue, settings, and generate images/videos "
        "via Leonardo AI's backend. Server must be running at "
        + BASE_URL
    ),
)

client = httpx.Client(base_url=BASE_URL, timeout=TIMEOUT)


def _get(path: str) -> dict | list:
    r = client.get(path)
    r.raise_for_status()
    return r.json()


def _post(path: str, body: dict | list | None = None) -> dict | list:
    r = client.post(path, json=body)
    r.raise_for_status()
    return r.json()


def _put(path: str, body: dict) -> dict:
    r = client.put(path, json=body)
    r.raise_for_status()
    return r.json()


def _patch(path: str, body: dict | None = None) -> dict:
    r = client.patch(path, json=body or {})
    r.raise_for_status()
    return r.json()


def _delete(path: str) -> dict:
    r = client.delete(path)
    r.raise_for_status()
    return r.json()


# ═══════════════════════════════════════════════════════════════════════════════
# Health
# ═══════════════════════════════════════════════════════════════════════════════


@mcp.tool()
def health_check() -> str:
    """Check if LeoStudio server is running and healthy."""
    return json.dumps(_get("/health"))


# ═══════════════════════════════════════════════════════════════════════════════
# Image Generation
# ═══════════════════════════════════════════════════════════════════════════════


@mcp.tool()
def generate_image(
    prompt: str,
    model: str = "",
    n: int = 1,
    aspect_ratio: str = "",
    image_url: str = "",
    style: str = "",
) -> str:
    """
    Generate images from a text prompt using Leonardo AI.

    Args:
        prompt: Text description of the image to generate (required)
        model: Leonardo model name or UUID (optional, uses default if empty)
        n: Number of images to generate (1-4, default 1)
        aspect_ratio: Aspect ratio — "16:9", "9:16", "1:1", "4:3" (optional)
        image_url: URL of a reference image for img2img (optional)
        style: Style name — "cinematic", "creative", "dynamic", "fashion",
               "portrait", "stock photo", "vibrant", or "none" (optional,
               default is "dynamic")
    """
    body = {"prompt": prompt, "n": n}
    if model:
        body["model"] = model
    if aspect_ratio:
        body["aspect_ratio"] = aspect_ratio
    if image_url:
        body["image_url"] = image_url
    if style:
        body["style"] = style
    return json.dumps(_post("/v1/images/generations", body))


# ═══════════════════════════════════════════════════════════════════════════════
# Video Generation
# ═══════════════════════════════════════════════════════════════════════════════


@mcp.tool()
def generate_video(
    prompt: str,
    model: str = "",
    aspect_ratio: str = "",
    resolution: str = "",
    duration: int = 0,
    audio: bool = False,
    image_url: str = "",
) -> str:
    """
    Generate a video from a text prompt using Leonardo AI (Seedance).

    Args:
        prompt: Text description of the video to generate (required)
        model: Video model slug, e.g. "seedance-2.0" or "seedance-2.0-fast" (optional)
        aspect_ratio: "16:9", "9:16", "1:1", "4:3" (optional)
        resolution: "480p", "720p", or "1080p" (optional)
        duration: Duration in seconds, 4-15 (optional, uses model default)
        audio: Whether to generate audio (default False)
        image_url: URL of a start-frame image for image-to-video (optional)
    """
    body = {"prompt": prompt}
    if model:
        body["model"] = model
    if aspect_ratio:
        body["aspect_ratio"] = aspect_ratio
    if resolution:
        body["resolution"] = resolution
    if duration:
        body["duration"] = duration
    if audio:
        body["audio"] = True
    if image_url:
        body["image_url"] = image_url
    return json.dumps(_post("/v1/videos/generations", body))


# ═══════════════════════════════════════════════════════════════════════════════
# Cookie Management
# ═══════════════════════════════════════════════════════════════════════════════


@mcp.tool()
def list_cookies() -> str:
    """List all Leonardo AI cookies in the pool with their status and balance."""
    return json.dumps(_get("/api/cookies"))


@mcp.tool()
def add_cookie(value: str) -> str:
    """
    Add a new Leonardo AI cookie to the rotation pool.

    Args:
        value: Full cookie string (from ExLeo extension) or token=JWT format
    """
    return json.dumps(_post("/api/cookies", {"value": value}))


@mcp.tool()
def update_cookie(cookie_id: int, value: str) -> str:
    """
    Replace an existing cookie's auth payload.

    Args:
        cookie_id: ID of the cookie to update
        value: New cookie string
    """
    return json.dumps(_put(f"/api/cookies/{cookie_id}", {"value": value}))


@mcp.tool()
def delete_cookie(cookie_id: int) -> str:
    """
    Remove a cookie from the pool permanently.

    Args:
        cookie_id: ID of the cookie to delete
    """
    return json.dumps(_delete(f"/api/cookies/{cookie_id}"))


@mcp.tool()
def toggle_cookie(cookie_id: int, enabled: bool) -> str:
    """
    Enable or disable a cookie without deleting it.

    Args:
        cookie_id: ID of the cookie to toggle
        enabled: True to enable, False to disable
    """
    return json.dumps(_patch(f"/api/cookies/{cookie_id}/toggle", {"enabled": enabled}))


@mcp.tool()
def refresh_cookie_profiles() -> str:
    """Refresh email and balance for all cookies. Depleted/expired cookies get auto-disabled."""
    return json.dumps(_post("/api/cookies/refresh-profiles"))


@mcp.tool()
def refresh_cookie_sessions() -> str:
    """Re-resolve JWT tokens for all cookies via TLS impersonation."""
    return json.dumps(_post("/api/cookies/refresh-sessions"))


@mcp.tool()
def cookie_health() -> str:
    """Get aggregated cookie pool health: total, active, inactive, depleted counts."""
    return json.dumps(_get("/api/cookies/health"))


# ═══════════════════════════════════════════════════════════════════════════════
# Settings
# ═══════════════════════════════════════════════════════════════════════════════


@mcp.tool()
def get_setting(key: str, default: str = "") -> str:
    """
    Read a LeoStudio setting value.

    Args:
        key: Setting key, e.g. "default_aspect_ratio", "auto_save_images"
        default: Fallback value if key doesn't exist
    """
    params = f"?default={default}" if default else ""
    return json.dumps(_get(f"/api/settings/{key}{params}"))


@mcp.tool()
def set_setting(key: str, value: str) -> str:
    """
    Write a LeoStudio setting.

    Args:
        key: Setting key to set
        value: New value
    """
    return json.dumps(_put(f"/api/settings/{key}", {"value": value}))


# ═══════════════════════════════════════════════════════════════════════════════
# Models
# ═══════════════════════════════════════════════════════════════════════════════


@mcp.tool()
def list_image_models() -> str:
    """List all available image models (official catalog + custom UUIDs)."""
    return json.dumps(_get("/api/models/images"))


@mcp.tool()
def add_image_model(model_id: str, name: str = "") -> str:
    """
    Add a custom image model by Leonardo UUID.

    Args:
        model_id: Leonardo model UUID
        name: Display name (optional, derived from UUID if empty)
    """
    body = {"model_id": model_id}
    if name:
        body["name"] = name
    return json.dumps(_post("/api/models/images", body))


@mcp.tool()
def delete_image_model(model_id: int) -> str:
    """
    Remove an image model from the local catalog.

    Args:
        model_id: Database ID of the model row
    """
    return json.dumps(_delete(f"/api/models/images/{model_id}"))


@mcp.tool()
def set_default_image_model(model_id: int) -> str:
    """
    Set which image model is used by default when none is specified.

    Args:
        model_id: Database ID of the model to make default
    """
    return json.dumps(_put(f"/api/models/images/{model_id}/default", {}))


@mcp.tool()
def sync_image_models() -> str:
    """Pull the official Leonardo image catalog and update the local database."""
    return json.dumps(_post("/api/models/sync"))


@mcp.tool()
def list_video_models() -> str:
    """List all supported video models (Seedance, etc.) with their constraints."""
    return json.dumps(_get("/api/models/videos"))


# ═══════════════════════════════════════════════════════════════════════════════
# Queue
# ═══════════════════════════════════════════════════════════════════════════════


@mcp.tool()
def enqueue_jobs(specs: list[dict]) -> str:
    """
    Add one or more generation jobs to the processing queue.

    Args:
        specs: List of job specs, each with:
            - type: "image" or "video" (required)
            - prompt: Text prompt (required)
            - model_id: Model UUID or video slug (optional)
            - aspect_ratio: "16:9", "9:16", "1:1", "4:3" (optional)
            - resolution: "480p"/"720p"/"1080p" video only (optional)
            - duration: seconds, video only (optional)
            - audio: bool, video only (optional)
            - quantity: number of images, image only (optional, default 1)
            - ref_image_ids: list of pre-uploaded init image IDs (optional)
    """
    return json.dumps(_post("/api/queue/enqueue", specs))


@mcp.tool()
def list_queue() -> str:
    """List all queued jobs (pending, running, completed, failed)."""
    return json.dumps(_get("/api/queue"))


@mcp.tool()
def cancel_queue_job(job_id: int) -> str:
    """
    Cancel a pending queue job. Running jobs cannot be canceled.

    Args:
        job_id: ID of the job to cancel
    """
    return json.dumps(_delete(f"/api/queue/{job_id}"))


@mcp.tool()
def retry_queue_job(job_id: int) -> str:
    """
    Re-queue a failed or canceled job.

    Args:
        job_id: ID of the job to retry
    """
    return json.dumps(_post(f"/api/queue/{job_id}/retry"))


@mcp.tool()
def clear_finished_jobs() -> str:
    """Remove all completed, failed, and canceled jobs from the queue."""
    return json.dumps(_delete("/api/queue/finished"))


# ═══════════════════════════════════════════════════════════════════════════════
# Library / Logs
# ═══════════════════════════════════════════════════════════════════════════════


@mcp.tool()
def list_generation_logs(limit: int = 50) -> str:
    """
    List recent generation history (newest first).

    Args:
        limit: Max number of logs to return (default 50, max 200)
    """
    return json.dumps(_get(f"/api/logs?limit={limit}"))


# ═══════════════════════════════════════════════════════════════════════════════
# Aspect Ratios
# ═══════════════════════════════════════════════════════════════════════════════


@mcp.tool()
def list_aspect_ratios() -> str:
    """List all supported aspect ratios with their pixel dimensions."""
    return json.dumps(_get("/api/aspects"))


# ═══════════════════════════════════════════════════════════════════════════════
# Entry point
# ═══════════════════════════════════════════════════════════════════════════════

if __name__ == "__main__":
    mcp.run(transport="stdio")
