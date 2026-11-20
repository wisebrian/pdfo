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

    // Phase 2: PDFs.
    const get = async (n) => new Uint8Array(await (await fetch(n)).arrayBuffer());
    const basic = await get("basic.pdf"), forms = await get("lo-forms.pdf"), locked = await get("locked.pdf");
    const isPDF = (b) => b instanceof Uint8Array && String.fromCharCode(...b.subarray(0, 5)) === "%PDF-";
    r = pdfo.info(basic);
    let inf = r.report && JSON.parse(r.report);
    check("info", inf && inf.pages.length === 3 && inf.pages[0].sizeName === "A4" && inf.bookmarks && inf.javaScript, r);
    r = pdfo.merge([basic, forms]);
    check("merge", isPDF(r.pdf) && JSON.parse(r.report).pages === 5, r.error);
    r = pdfo.split(basic, {every: 1});
    check("split every", Array.isArray(r.pdfs) && r.pdfs.length === 3 && r.pdfs.every(isPDF) && JSON.parse(r.report).parts.length === 3, r.error);
    r = pdfo.split(basic, {pages: "1-2, 3"});
    check("split ranges", r.pdfs && r.pdfs.length === 2, r.error);
    r = pdfo.extract(basic, {pages: "3, 1"});
    check("extract", isPDF(r.pdf) && JSON.parse(pdfo.info(r.pdf).report).pages[0].sizeName === "A5", r.error);
    r = pdfo.delete(basic, {pages: [2]});
    check("delete", isPDF(r.pdf) && JSON.parse(r.report).pages === 2, r.error);
    r = pdfo.reorder(basic, {order: [3, 2, 1]});
    check("reorder", isPDF(r.pdf), r.error);
    r = pdfo.rotate(basic, {pages: "odd", degrees: 90});
    check("rotate", isPDF(r.pdf) && JSON.parse(pdfo.info(r.pdf).report).pages[0].rotate === 90, r.error);
    r = pdfo.resize(basic, {size: "Letter"});
    check("resize", isPDF(r.pdf) && JSON.parse(pdfo.info(r.pdf).report).pages.every(p => p.sizeName === "Letter"), r.error);
    r = pdfo.assemble([basic, forms], {pages: [{doc: 1, page: 1}, {doc: 0, page: 2, rotate: 180, size: "A4"}]});
    check("assemble", isPDF(r.pdf) && JSON.parse(r.report).pages === 2, r.error);
    r = pdfo.parsePages("1-3, last", 5);
    check("parsePages", r.pages === "[[1,2,3],[5]]", r);
    r = pdfo.parsePages("1, 9", 5);
    check("parsePages error", r.code === "bad-argument" && r.pos === 3, r);
    r = pdfo.extract(basic, {pages: "1-9"});
    check("page out of range", r.code === "bad-argument" && /does not exist/.test(r.error), r);
    r = pdfo.info(locked);
    check("password required", r.code === "password-required", r);
    r = pdfo.info(locked, {password: "nope"});
    check("wrong password", r.code === "wrong-password", r);
    r = pdfo.merge([basic, locked], {passwords: ["", "user"]});
    check("merge with password", isPDF(r.pdf) && JSON.parse(r.report).inputEncrypted === true, r.error);
    r = pdfo.merge([basic, locked]);
    check("merge needs password", r.code === "password-required" && r.index === 1, r);
    const photos = pdfo.imagesToPDF([photo, photo], {pageSize: "A6"});
    r = pdfo.compress(photos.pdf, {preset: "screen"});
    const crep = r.report && JSON.parse(r.report);
    check("compress", isPDF(r.pdf) && crep.compression && crep.compression.images.length === 1 && crep.compression.sizeBefore === photos.pdf.length, r.error || r.report);
    r = pdfo.compress(basic, {preset: "fast"});
    check("compress bad preset", r.code === "bad-argument" && r.option === "preset", r);
    r = pdfo.info(new TextEncoder().encode("not a pdf"));
    check("damaged pdf", r.code === "damaged", r);

    r = pdfo.imagesToPDF([photo], {get pageSize() { throw new Error("boom from a getter"); }});
    check("panic recovered", r.code === "internal" && /boom/.test(r.error), r);
    r = pdfo.imagesToPDF([photo]);
    check("alive after panic", r.pdf instanceof Uint8Array, r.error);
  } catch (e) {
    check("exception escaped", false, String(e && e.stack || e));
  }
  postMessage(results);
})();
