const SERVER = "http://127.0.0.1:8001";
const DOMAIN = "app.leonardo.ai";

document.getElementById("btn").addEventListener("click", async () => {
  const btn = document.getElementById("btn");
  const status = document.getElementById("status");
  btn.disabled = true;
  status.className = "";
  status.textContent = "Reading cookies...";

  try {
    const cookies = await chrome.cookies.getAll({ domain: DOMAIN });
    if (!cookies.length) {
      status.className = "err";
      status.textContent = "No cookies found. Log in to app.leonardo.ai first.";
      btn.disabled = false;
      return;
    }

    // Build cookie string: name=value; name=value; ...
    const cookieStr = cookies.map(c => `${c.name}=${c.value}`).join("; ");
    
    status.textContent = `Found ${cookies.length} cookies. Sending...`;

    const resp = await fetch(`${SERVER}/import`, {
      method: "POST",
      headers: { "Content-Type": "text/plain" },
      body: cookieStr,
    });

    const result = await resp.json();
    if (result.ok) {
      status.className = "ok";
      status.textContent = `Done! Pool: ${result.total} cookie(s)`;
    } else {
      status.className = "err";
      status.textContent = result.error || "Server error";
    }
  } catch (e) {
    status.className = "err";
    if (e.message.includes("Failed to fetch")) {
      status.textContent = "Import server not running. Start it: python3 import_server.py";
    } else {
      status.textContent = e.message;
    }
  }
  btn.disabled = false;
});
