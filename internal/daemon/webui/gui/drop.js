// guardPageDrops: a file dropped anywhere but a send box must not turn
// the page into that file (the browser's default for a drop is to open
// it). Only the send boxes take images (the session's #d-send and the
// Dashboard's tapi panel #c-send): their own dragover/drop handlers call
// preventDefault first, so by the time the event bubbles up to target
// (the window) it is already handled. Every other drag that carries files
// has its default stopped here, and dragover says "none" so the cursor
// shows the drop is not taken. A drag without files (text being moved
// into a box) keeps its default.

export const carriesFiles = (dt) => !!dt && Array.from(dt.types || []).includes("Files");

export function guardPageDrops(target) {
  const stop = (e) => {
    if (e.defaultPrevented || !carriesFiles(e.dataTransfer)) return;
    e.preventDefault();
    if (e.type === "dragover") e.dataTransfer.dropEffect = "none";
  };
  target.addEventListener("dragover", stop);
  target.addEventListener("drop", stop);
}
