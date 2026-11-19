// Feeds about 200 MB of JPEGs to imagesToPDF and reports how much the
// WebAssembly memory grew: the inputs' Go copy is the only large thing the
// Go side should hold, since the output is written straight into JavaScript.
importScripts("wasm_exec.js");
(async () => {
  const out = {};
  try {
    const go = new Go();
    const {instance} = await WebAssembly.instantiateStreaming(fetch("pdfo.wasm"), go.importObject);
    go.run(instance);
    const mem = () => instance.exports.mem.buffer.byteLength;
    const photo = new Uint8Array(await (await fetch("big.jpg")).arrayBuffer());
    const n = Math.ceil(200e6 / photo.length);
    const images = Array.from({length: n}, () => photo.slice());
    out.inputBytes = n * photo.length;
    out.images = n;
    out.memBefore = mem();
    const t0 = performance.now();
    const r = pdfo.imagesToPDF(images, {pageSize: "A4"});
    out.ms = Math.round(performance.now() - t0);
    out.memAfter = mem();
    out.outputBytes = r.pdf ? r.pdf.length : 0;
    out.error = r.error;
    const t1 = performance.now();
    const r2 = pdfo.imagesToPDF(images, {pageSize: "A4"});
    out.ms2 = Math.round(performance.now() - t1);
    out.memAfterSecondCall = mem();
    out.same = r2.pdf && r2.pdf.length === r.pdf.length;
  } catch (e) {
    out.exception = String(e && e.stack || e);
  }
  postMessage([{name: "memory", ok: !out.exception && !out.error, detail: out}]);
})();
