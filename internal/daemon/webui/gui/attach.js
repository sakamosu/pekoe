// attach.js: the images waiting for each composer's next send. A key per
// composer: a session's send box by its slug (tapi's tab, #/tapi, by
// TAPI), the Dashboard's tapi panel by a key of its own (gui.js PANEL), so
// what was attached in one never rides along with another (lib/images.js:
// kept per target, like the drafts).
//
// add takes the images among files, at most max per key (a toast for the
// rest), each through prepare (lib/images.js prepareImage: read and drawn
// again); changed(key) after each change, so the page draws the thumbs.

export function attachments({ max, prepare, toast, changed }) {
  const by = new Map(); // key → prepared images
  const reading = new Set(); // keys whose files are being read
  const get = (key) => (key != null && by.get(key)) || [];
  const put = (key, images) => {
    if (images.length) by.set(key, images);
    else by.delete(key);
    changed(key);
  };
  return {
    get,
    preparing: (key) => reading.has(key),
    drop: (key, images) => put(key, get(key).filter((im) => !images.includes(im))),
    async add(key, files) {
      files = files.filter((f) => !f.type || f.type.startsWith("image/"));
      if (key == null || !files.length || reading.has(key)) return;
      const room = max - get(key).length;
      if (room <= 0) {
        toast("at most " + max + " images in one send");
        return;
      }
      if (files.length > room) toast("at most " + max + " images in one send: took the first " + room);
      reading.add(key);
      changed(key);
      const got = [];
      for (const f of files.slice(0, room)) {
        try {
          got.push(await prepare(f));
        } catch (e) {
          toast(e.message);
        }
      }
      reading.delete(key);
      put(key, [...get(key), ...got]);
    },
  };
}
