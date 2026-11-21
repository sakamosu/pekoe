// shellBang is pekoed's shellBang (internal/daemon/human.go): after the
// control characters engine.CleanText drops and the leading white space,
// the text starts with "!", which Claude Code's input box runs as a shell
// command (docs/gui-design.md 決まったこと 6). The page puts such a send
// through the shell sheet. JS's \s also takes U+FEFF, which Go's
// unicode.IsSpace does not: the sheet then comes up once more than needed,
// never once less (TestGUIShellBangMatchesPekoed).
export function shellBang(text) {
  const clean = String(text).replace(/\r\n?/g, "\n").replace(/[\u0000-\u0008\u000b-\u001f\u007f-\u009f]/g, "");
  return clean.replace(/^\s+/, "").startsWith("!");
}
