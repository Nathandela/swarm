"""Research only: fake-microphone browser capability checks; never records a real mic."""
import json
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from playwright.sync_api import sync_playwright

WORKLET = """
class Probe extends AudioWorkletProcessor {
  process(inputs) {
    if (inputs[0] && inputs[0][0]) {
      this.port.postMessage({samples: inputs[0][0].length, rate: sampleRate});
    }
    return true;
  }
}
registerProcessor('probe', Probe);
"""

class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        body = WORKLET if self.path == '/worklet.js' else '<!doctype html><title>Swarm fake audio probe</title>'
        self.send_response(200)
        self.send_header('Content-Type', 'text/javascript' if self.path == '/worklet.js' else 'text/html')
        self.end_headers()
        self.wfile.write(body.encode())

    def log_message(self, *_):
        pass

CHECK = """() => ({secure: isSecureContext, media: !!navigator.mediaDevices,
  microphone: !!navigator.mediaDevices?.getUserMedia, worklet: 'AudioWorkletNode' in window})"""
CAPTURE = """async () => {
  const stream = await navigator.mediaDevices.getUserMedia({audio: {
    echoCancellation: true, noiseSuppression: true, autoGainControl: true}});
  const ctx = new AudioContext({sampleRate: 24000});
  try {
    await ctx.audioWorklet.addModule('/worklet.js');
    const source = ctx.createMediaStreamSource(stream);
    const node = new AudioWorkletNode(ctx, 'probe');
    const gain = ctx.createGain(); gain.gain.value = 0;
    node.connect(gain).connect(ctx.destination); source.connect(node);
    const result = new Promise((resolve, reject) => {
      let samples = 0, blocks = 0;
      const timer = setTimeout(() => reject(Error('fake audio timeout')), 5000);
      node.port.onmessage = ({data}) => {
        samples += data.samples; blocks++;
        if (samples >= 24000) {
          clearTimeout(timer);
          resolve({contextRate: ctx.sampleRate, trackRate: stream.getAudioTracks()[0].getSettings().sampleRate,
            samples, blocks, workletRate: data.rate});
        }
      };
    });
    await ctx.resume();
    return await result;
  } finally {stream.getTracks().forEach(t => t.stop()); await ctx.close();}
}"""

def main():
    server = ThreadingHTTPServer(('127.0.0.1', 0), Handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    results = {'kind': 'desktop-chromium-fake-device', 'real_microphone': False}
    try:
        with sync_playwright() as pw:
            browser = pw.chromium.launch(args=['--use-fake-device-for-media-stream', '--use-fake-ui-for-media-stream'])
            try:
                results['browser'] = browser.version
                page = browser.new_page()
                port = server.server_port
                for host in ['127.0.0.1', 'localhost']:
                    page.goto(f'http://{host}:{port}')
                    result = page.evaluate(CHECK)
                    assert all(result.values()), (host, result)
                    result['audio'] = page.evaluate(CAPTURE)
                    assert result['audio']['contextRate'] == result['audio']['workletRate'] == 24000
                    results[host] = result
                page.route('http://192.0.2.1/**', lambda route: route.fulfill(status=200, content_type='text/html', body='<!doctype html><title>insecure HTTP control</title>'))
                page.goto('http://192.0.2.1/')
                result = page.evaluate(CHECK)
                assert not result['secure'] and not result['microphone'], result
                results['nonloopback_http_control'] = result
            finally:
                browser.close()
    finally:
        server.shutdown()
        server.server_close()
        thread.join()
    print(json.dumps(results, indent=2))

if __name__ == '__main__':
    main()
