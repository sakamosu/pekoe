# boba Chrome 拡張

ブラウザで要素を選び、指示と一緒に boba のセッションへ送る Chrome MV3 拡張です。ビルド不要の素の MV3 で書いています。

## インストール

1. `chrome://extensions` を開く
2. 右上の「デベロッパーモード」をオンにする
3. 「パッケージ化されていない拡張機能を読み込む」でこの `extension/` ディレクトリを選ぶ
4. 拡張の「詳細」→「拡張機能のオプション」を開き、`boba ext` が表示する port と token を入力して「保存」→「接続テスト」

ショートカットは `chrome://extensions/shortcuts` で変更できます(既定: macOS ⌘⇧E / その他 Ctrl+Shift+E)。

## 使い方

1. ⌘⇧E(またはツールバーの boba アイコン)で選択モードに入る
2. ホバーでハイライト、クリックで確定、Esc で中止
3. 出てきた入力欄に指示を書いて Enter(空のまま Enter なら要素情報だけ送る)。Esc で中止
4. 切り抜きスクショ付きで bobad に送られ、バッジに ✓ / ✗ が一瞬出る

localhost / 127.0.0.1 のページでは `window.onerror`・`unhandledrejection`・`console.error` を拾って bobad に送ります。開いている localhost タブの一覧も送ります。

## ファイル

| ファイル | 役割 |
| --- | --- |
| `manifest.json` | MV3 マニフェスト |
| `background.js` | service worker。スクショ撮影・切り抜き、bobad への POST(`/pick` `/console` `/tabs`) |
| `content.js` | isolated world。選択 UI(closed shadow root)、outerHTML・スタイル・座標の収集 |
| `page.js` | MAIN world。React / Vue / Svelte のコンポーネント名とソース位置の検出、console の捕捉 |
| `options.html` / `options.js` | port と token の設定、`GET /health` の接続テスト |

API は `docs/api.md` の「2. HTTP(Chrome 拡張)」を参照。
