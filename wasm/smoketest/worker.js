importScripts("wasm_exec.js");
const results = [];
function check(name, ok, detail) { results.push({name, ok: !!ok, detail}); }
(async () => {
  try {
    const go = new Go();
    const {instance} = await WebAssembly.instantiateStreaming(fetch("pdfo.wasm"), go.importObject);
    go.run(instance);
    check("global object", typeof self.pdfo === "object" && typeof self.pdfo.imagesToPDF === "function");
    const photo = new Uint8Array(await (await fetch("photo.jpg")).arrayBuffer());

    let r = pdfo.imagesToPDF([photo, photo], {pageSize: "A4", margin: "10mm", orientation: "portrait", timestamp: "2026-11-19T21:05:00Z"});
    const head = r.pdf ? String.fromCharCode(...r.pdf.subarray(0, 8)) : "";
    const tail = r.pdf ? String.fromCharCode(...r.pdf.subarray(r.pdf.length - 6)) : "";
    check("success", r.pdf instanceof Uint8Array && head === "%PDF-1.7" && tail === "%%EOF\n", {head, len: r.pdf && r.pdf.length, error: r.error});
    const rep = JSON.parse(r.report);
    check("report", rep.pages === 2 && Array.isArray(rep.warnings), r.report);

    let again = pdfo.imagesToPDF([photo, photo], {pageSize: "A4", margin: "10mm", orientation: "portrait", timestamp: "2026-11-19T21:05:00Z"});
    check("deterministic", again.pdf.length === r.pdf.length && again.pdf.every((b, i) => b === r.pdf[i]));

    r = pdfo.imagesToPDF("not an array");
    check("bad argument", r.code === "bad-argument" && !r.pdf, r);
    r = pdfo.imagesToPDF([photo, "x"]);
    check("bad element", r.code === "bad-argument", r);
    r = pdfo.imagesToPDF([photo], {pageSise: "A4"});
    check("misspelt option", r.code === "bad-argument" && /pageSise/.test(r.error), r);
    r = pdfo.imagesToPDF([photo], {pageSize: "A9"});
    check("bad option value", r.code === "bad-argument" && r.option === "pageSize", r);
    r = pdfo.imagesToPDF([photo, new TextEncoder().encode("GIF89a......")]);
    check("unsupported", r.code === "unsupported" && r.index === 1, r);
    r = pdfo.imagesToPDF([photo.subarray(0, 40)]);
    check("damaged", r.code === "damaged" && r.index === 0, r);

    r = pdfo.imagesToPDF([photo], {get pageSize() { throw new Error("boom from a getter"); }});
    check("panic recovered", r.code === "internal" && /boom/.test(r.error), r);
    r = pdfo.imagesToPDF([photo]);
    check("alive after panic", r.pdf instanceof Uint8Array, r.error);
  } catch (e) {
    check("exception escaped", false, String(e && e.stack || e));
  }
  postMessage(results);
})();
