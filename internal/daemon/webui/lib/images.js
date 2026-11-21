// Images attached to a send.
// Each is read here and drawn again on a canvas, as JPEG (PNG stays PNG,
// for a screenshot's text), at most IMG_SIDE pixels on a side: a HEIC
// photo becomes a JPEG the agent can read, the location and the rest of
// the photo's metadata stay on the device, and the upload stays small.
// pekoed checks each again (PNG / JPEG / GIF by its bytes, the limits) and
// names the file itself. The pages keep them per target, like the drafts:
// one session's images never ride along to another.

import { el } from "./dom.js";
import { kb } from "./fmt.js";

export const IMG_MAX = 4; // images in one send (pekoed: webMaxImages)
const IMG_SIDE = 2048; // pixels on the long side
const IMG_BYTES = 5 * 1024 * 1024; // one image (pekoed: webMaxImageBytes)

function readDataURL(file) {
  return new Promise((ok, fail) => {
    const r = new FileReader();
    r.onload = () => ok(String(r.result));
    r.onerror = () => fail(new Error("could not read " + (file.name || "the file")));
    r.readAsDataURL(file);
  });
}

function loadImage(url) {
  return new Promise((ok, fail) => {
    const img = new Image();
    img.onload = () => ok(img);
    img.onerror = () => fail(new Error("not an image this browser can open"));
    img.src = url;
  });
}

const b64Bytes = (url) => Math.floor((url.length - url.indexOf(",") - 1) * 3 / 4);

// draw: img on a canvas of at most side pixels, as type (a data URL)
function draw(img, side, type, quality) {
  const w0 = img.naturalWidth, h0 = img.naturalHeight;
  const k = Math.min(1, side / Math.max(w0, h0));
  const c = document.createElement("canvas");
  c.width = Math.max(1, Math.round(w0 * k));
  c.height = Math.max(1, Math.round(h0 * k));
  const g = c.getContext("2d");
  if (type === "image/jpeg") {
    g.fillStyle = "#fff"; // JPEG has no transparency
    g.fillRect(0, 0, c.width, c.height);
  }
  g.drawImage(img, 0, 0, c.width, c.height);
  return { url: c.toDataURL(type, quality), w: c.width, h: c.height };
}

export async function prepareImage(file) {
  if (file.type && !file.type.startsWith("image/")) throw new Error((file.name || "a file") + " is not an image");
  const img = await loadImage(await readDataURL(file));
  if (!img.naturalWidth || !img.naturalHeight) throw new Error("not an image this browser can open");
  let type = file.type === "image/png" ? "image/png" : "image/jpeg";
  let out = draw(img, IMG_SIDE, type, 0.85);
  if (type === "image/png" && b64Bytes(out.url) > IMG_BYTES) {
    type = "image/jpeg"; // a photo saved as PNG
    out = draw(img, IMG_SIDE, type, 0.85);
  }
  for (const q of [0.7, 0.5]) {
    if (b64Bytes(out.url) <= IMG_BYTES) break;
    out = draw(img, IMG_SIDE, "image/jpeg", q);
    type = "image/jpeg";
  }
  const bytes = b64Bytes(out.url);
  if (bytes > IMG_BYTES) throw new Error("an image is too large even at " + IMG_SIDE + "px");
  return {
    data: out.url.slice(out.url.indexOf(",") + 1),
    thumb: draw(img, 160, "image/jpeg", 0.7).url,
    w: out.w, h: out.h, bytes, type,
  };
}

// thumbs: the images as small pictures, each with × to take it out
// (onRemove(image), the composer) or without (the sheet: no onRemove)
export function thumbs(box, images, onRemove) {
  box.replaceChildren();
  images.forEach((im) => {
    const t = el("span", "thumb");
    const img = el("img");
    img.alt = "image " + im.w + "×" + im.h;
    img.src = im.thumb; // a data: URL drawn here, from the device's own file
    t.append(img, el("span", "size", kb(im.bytes)));
    if (onRemove) {
      const x = el("button", "x", "×");
      x.type = "button";
      x.setAttribute("aria-label", "remove the image");
      x.addEventListener("click", () => onRemove(im));
      t.append(x);
    }
    box.append(t);
  });
  box.hidden = !images.length;
}

// body of a send: the text, and the prepared images' data
export function sendBody(text, images) {
  const body = { text };
  if (images.length) body.images = images.map((im) => ({ data: im.data }));
  return body;
}

export const imagesNote = (n) => (n ? " (" + n + " image" + (n > 1 ? "s" : "") + ")" : "");
