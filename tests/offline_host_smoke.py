#!/usr/bin/env python3
"""Exercise the actual CPA host/plugin ABI without contacting any upstream.

Builds a COPY with an in-memory, deny-by-default HTTP transport. Never install
that diagnostic library: it returns synthetic reasoning and captured parameters.
The caller supplies a CGO-enabled CPA executable; no live account is read.
"""
import argparse
import json
import os
from pathlib import Path
import shutil
import socket
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request

# Independent expectations from the channel table. Native IDs can differ from
# UI names: Kimi K3-1/K3-2 and a discovered opaque ID are covered explicitly.
SPECS = [
    ("deepseek-v4.1-flash", "DeepSeek-V4.1-Flash", ["low", "high", "max"], "high"),
    ("space-bunny", "Space-Bunny", ["low", "medium", "high", "xhigh", "max"], "max"),
    ("minimax-m3.1-flash", "MiniMax-M3.1-Flash", ["low", "medium", "high", "xhigh", "max"], "max"),
    ("step-5-preview", "Step-5-Preview", ["low", "medium", "high"], "high"),
    ("kimi-k3-2", "Kimi-K3", ["low", "high", "xhigh"], "high"),
    ("kimi-k3-1", "Kimi-K3", ["low", "high", "xhigh"], "high"),
    ("glm-5.3", "GLM-5.3", ["low", "high", "max"], "high"),
    ("glm-5.3-flash", "GLM-5.3-Flash", ["low", "high", "max"], "high"),
    ("kimi-k2.8-preview", "Kimi-K2.8-Preview", ["low", "high", "max"], "high"),
    ("kimi-k3.1-pro", "Kimi-K3.1-Pro", ["low", "high", "max"], "high"),
    ("glm-5.2", "GLM-5.2", ["high", "xhigh"], "high"),
    ("deepseek-v4-flash", "DeepSeek-V4-Flash", ["high", "xhigh"], "high"),
    ("deepseek-v4-pro", "DeepSeek-V4-Pro", ["high", "xhigh"], "high"),
    ("hy3", "Hy3", ["low", "high"], "high"),
    # Legacy opaque capabilities: these choices exercise passthrough, not a
    # claim that their complete supported range is known.
    ("hy3-preview", "Hy3 Preview", ["low", "high"], "high"),
    ("hy3-preview-agent", "Hy3 Preview Agent", ["low", "high"], "high"),
    ("hy4-preview", "Hy4 preview", ["high"], "high"),
    ("hy4", "Hy4", ["high"], "high"),
    ("mimo-v2.6-pro", "Xiaomi MiMo-V2.6-Pro", ["high"], "high"),
    ("opaque-channel-id", "DeepSeek-V4.1-Flash", ["low", "high", "max"], "high"),
]

TRANSPORT = r'''
package main
import("bytes";"encoding/json";"fmt";"io";"net/http")
type offlineTransport struct{}
func(offlineTransport)RoundTrip(r *http.Request)(*http.Response,error){
 var data string
 if r.URL.Path=="/v3/config" {
  data=CATALOG_JSON
 }else if r.URL.Path=="/v2/chat/completions" {
  var in map[string]any
  if err:=json.NewDecoder(r.Body).Decode(&in);err!=nil{return nil,err}
  capture:=map[string]any{}
  for _,key:=range []string{"model","reasoning_effort","reasoning_summary","thinking"}{if v,ok:=in[key];ok{capture[key]=v}}
  raw,_:=json.Marshal(capture)
  first,_:=json.Marshal(map[string]any{"id":"offline","object":"chat.completion.chunk","model":in["model"],"choices":[]any{map[string]any{"index":0,"delta":map[string]any{"role":"assistant","reasoning_content":"synthetic-reasoning"}}}})
  chunk,_:=json.Marshal(map[string]any{"id":"offline","object":"chat.completion.chunk","model":in["model"],"choices":[]any{map[string]any{"index":0,"delta":map[string]any{"content":string(raw)}}}})
  data="data: "+string(first)+"\n\ndata: "+string(chunk)+"\n\ndata: {\"id\":\"offline\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":3,\"total_tokens\":4,\"completion_tokens_details\":{\"reasoning_tokens\":2}}}\n\ndata: [DONE]\n\n"
 }else{return nil,fmt.Errorf("offline test blocked endpoint %s",r.URL.Path)}
 return &http.Response{StatusCode:200,Header:http.Header{"Content-Type":[]string{"text/event-stream"}},Body:io.NopCloser(bytes.NewBufferString(data)),Request:r},nil
}
func init(){sharedHTTPClient();sharedClient=&http.Client{Transport:offlineTransport{}}}
'''


def decode_result(route, stream, response):
    if not stream:
        obj = json.load(response)
        if route == "chat/completions":
            msg = obj["choices"][0]["message"]
            return json.loads(msg["content"]), msg.get("reasoning_content", ""), []
        outputs = obj.get("output", [])
        text = "".join(c.get("text", "") for o in outputs if o.get("type") == "message" for c in o.get("content", []))
        reasoning = "".join(c.get("text", "") for o in outputs if o.get("type") == "reasoning" for c in o.get("summary", []))
        return json.loads(text), reasoning, []
    text, reasoning, events = "", "", []
    for line in response:
        line = line.decode().strip()
        if not line.startswith("data:") or line[5:].strip() == "[DONE]":
            continue
        obj = json.loads(line[5:])
        event = obj.get("type", "")
        events.append(event)
        if event == "response.output_text.delta":
            text += obj.get("delta", "")
        if event == "response.reasoning_summary_text.delta":
            reasoning += obj.get("delta", "")
        for choice in obj.get("choices", []):
            delta = choice.get("delta", {})
            text += delta.get("content") or ""
            reasoning += delta.get("reasoning_content") or ""
    return json.loads(text), reasoning, events


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--cpa-core", required=True, type=Path)
    parser.add_argument("--source", type=Path, default=Path(__file__).resolve().parents[1])
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--go", default="go")
    args = parser.parse_args()
    args.output = args.output.resolve()
    args.output.mkdir(parents=True, exist_ok=True)
    results = []
    # Allocate only a fresh local port; never connect to/restart a live CPA.
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        port = s.getsockname()[1]
    with tempfile.TemporaryDirectory(prefix="offline-host-", dir=args.output) as temporary:
        root = Path(temporary)
        src = root / "src"
        src.mkdir()
        for p in args.source.iterdir():
            if p.is_file() and (p.suffix == ".go" or p.name in ("go.mod", "go.sum")):
                shutil.copy2(p, src / p.name)
        catalogue = {"code": 0, "data": {"models": [{"id": m, "name": n, "supportsReasoning": True, "onlyReasoning": True} for m, n, _, _ in SPECS]}}
        # Two JSON encodings create a valid Go string literal containing JSON.
        transport = TRANSPORT.replace("CATALOG_JSON", json.dumps(json.dumps(catalogue)))
        (src / "offline_transport.go").write_text(transport)
        plugin_dir = root / "plugins"
        plugin_dir.mkdir()
        suffix = ".dylib" if sys.platform == "darwin" else ".so"
        env = dict(os.environ, CGO_ENABLED="1", GOTOOLCHAIN="local", GOPROXY="off", GOSUMDB="off")
        subprocess.run([args.go, "build", "-mod=readonly", "-buildmode=c-shared", "-o", str(plugin_dir / ("workbuddy" + suffix)), "."], cwd=src, env=env, check=True)
        auth = root / "auth"
        auth.mkdir()
        (auth / "workbuddy.json").write_text(json.dumps({"type": "workbuddy", "disabled": False, "auth": {"accessToken": "offline-fake-token", "expiresAt": 4102444800000}, "account": {}}))
        (root / "home").mkdir()
        config = f'''config-version: 8
server:
  host: "127.0.0.1"
  port: {port}
  discovery:
    enabled: false
access:
  api-keys: ["offline-local-test-key"]
oauth:
  auth-dir: {json.dumps(str(auth))}
plugins:
  enabled: true
  dir: {json.dumps(str(plugin_dir))}
  configs:
    workbuddy:
      enabled: true
management:
  disable-control-panel: true
  disable-auto-update-panel: true
observability:
  logs:
    debug: true
    request-log: false
    logging-to-file: false
'''
        (root / "config.yaml").write_text(config)
        headers = {"Authorization": "Bearer offline-local-test-key", "Content-Type": "application/json"}
        base = f"http://127.0.0.1:{port}/v1/"
        hostenv = {"HOME": str(root / "home"), "PATH": os.environ.get("PATH", "/usr/bin:/bin"), "TMPDIR": os.environ.get("TMPDIR", "/tmp")}
        with (args.output / "host.log").open("wb") as log:
            proc = subprocess.Popen([str(args.cpa_core.resolve()), "-local-model", "-config", str(root / "config.yaml")], cwd=root, env=hostenv, stdout=log, stderr=subprocess.STDOUT)
            try:
                ready = False
                for _ in range(100):
                    if proc.poll() is not None:
                        break
                    try:
                        with urllib.request.urlopen(urllib.request.Request(base + "models", headers=headers), timeout=1) as response:
                            ids = {x["id"] for x in json.load(response)["data"]}
                        if all(m in ids for m, _, _, _ in SPECS):
                            ready = True
                            break
                    except (urllib.error.URLError, TimeoutError):
                        pass
                    time.sleep(0.1)
                if not ready:
                    raise RuntimeError("isolated host not ready; inspect host.log")

                def request(route, model, effort, stream, summary="auto"):
                    body = {"model": model, "stream": stream}
                    if route == "responses":
                        body["input"] = "offline fixture"
                        body["reasoning"] = {"summary": summary}
                        if effort is not None:
                            body["reasoning"]["effort"] = effort
                    else:
                        body["messages"] = [{"role": "user", "content": "offline fixture"}]
                        body["reasoning_summary"] = summary
                        if effort is not None:
                            body["reasoning_effort"] = effort
                    req = urllib.request.Request(base + route, data=json.dumps(body).encode(), headers=headers)
                    try:
                        with urllib.request.urlopen(req, timeout=15) as response:
                            capture, reasoning, events = decode_result(route, stream, response)
                            return {"status": response.status, "capture": capture, "reasoning": reasoning, "events": events}
                    except urllib.error.HTTPError as err:
                        return {"status": err.code, "error": err.read().decode()}

                # Regression first: this fails against the unmodified plugin.
                for model, _, efforts, default in SPECS:
                    for route in ("responses", "chat/completions"):
                        for stream in (True, False):
                            for effort in efforts + [None]:
                                result = request(route, model, effort, stream)
                                row = dict(model=model, route=route, stream=stream, input_effort=effort, **result)
                                results.append(row)
                                assert result["status"] == 200, row
                                capture = result["capture"]
                                assert capture.get("reasoning_effort") == (effort or default), row
                                assert capture.get("model") == model, row
                                assert capture.get("reasoning_summary") == "auto", row
                                assert "thinking" not in capture, row
                                assert result["reasoning"] == "synthetic-reasoning", row
                for route in ("responses", "chat/completions"):
                    for effort in ("medium", "xhigh", "ultra"):
                        result = request(route, "deepseek-v4.1-flash", effort, False)
                        row = dict(model="deepseek-v4.1-flash", route=route, input_effort=effort, **result)
                        results.append(row)
                        assert result["status"] >= 400 and "capture" not in result, row
                    result = request(route, "deepseek-v4.1-flash", "high", True, "none")
                    row = dict(model="deepseek-v4.1-flash", route=route, input_summary="none", **result)
                    results.append(row)
                    assert result["status"] == 200 and result["capture"]["reasoning_summary"] == "none", row
                print(f"PASS: {len(results)} actual-host cases; no real upstream calls.")
            finally:
                (args.output / "results.json").write_text(json.dumps(results, indent=2) + "\n")
                proc.terminate()
                try:
                    proc.wait(timeout=8)
                except subprocess.TimeoutExpired:
                    proc.kill()
                    proc.wait()
                print("Isolated CPA stopped; installed plugin and existing service unchanged.")


if __name__ == "__main__":
    main()
