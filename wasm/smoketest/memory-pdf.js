// Rotates and extracts the pages of a large PDF and reports how much the
// WebAssembly memory grew: the Go side should hold one copy of the input
// and little else, since stream data is written straight from it.
importScripts("wasm_exec.js");
(async () => {
  const out = {};
  try {
    const go = new Go();
    const {instance} = await WebAssembly.instantiateStreaming(fetch("pdfo.wasm"), go.importObject);
    go.run(instance);
    const mem = () => instance.exports.mem.buffer.byteLength;
    const pdf = new Uint8Array(await (await fetch("big.pdf")).arrayBuffer());
    out.inputBytes = pdf.length;
    out.memBefore = mem();
    let t = performance.now();
    let r = pdfo.info(pdf);
    out.infoMs = Math.round(performance.now() - t);
    out.pages = r.report && JSON.parse(r.report).pages.length;
    t = performance.now();
    r = pdfo.rotate(pdf, {degrees: 90});
    out.rotateMs = Math.round(performance.now() - t);
    out.rotateBytes = r.pdf ? r.pdf.length : r.error;
    out.memAfterRotate = mem();
    t = performance.now();
    r = pdfo.extract(pdf, {pages: "even"});
    out.extractMs = Math.round(performance.now() - t);
    out.memAfterExtract = mem();
    t = performance.now();
    r = pdfo.compress(pdf, {preset: "screen"});
    out.compressMs = Math.round(performance.now() - t);
    out.compressBytes = r.pdf ? r.pdf.length : r.error;
    out.memAfterCompress = mem();
    out.error = r.error;
  } catch (e) {
    out.exception = String(e && e.stack || e);
  }
  postMessage([{name: "memory", ok: !out.exception && !out.error, detail: out}]);
})();
