// Placeholder page for ready.3dl.dev/board (ready-2f1).
//
// This is intentionally NOT the board UI — it only proves the TypeScript
// build pipeline reaches production. The board UI itself is out of scope
// for this item (see docs/design/board-fold-spec.md for the eventual read
// projection this page will render).
//
// BUILD_STAMP is injected at build time via the VITE_BUILD_STAMP env var
// (set to the git commit SHA in CI, see .github/workflows/pages.yml). It
// gives the orchestrator a unique string to curl for after a merge to main
// lands a new build, proving the deployed page is the one just built and
// not a stale cache.
const BUILD_STAMP: string = import.meta.env.VITE_BUILD_STAMP ?? "dev-local";

function render(): void {
  const app = document.getElementById("app");
  if (!app) return;

  const heading = document.createElement("h1");
  heading.textContent = "ready — board";

  const subtitle = document.createElement("p");
  subtitle.textContent = "Placeholder page. Board UI lands in a later item.";

  const stamp = document.createElement("p");
  const stampLabel = document.createElement("code");
  stampLabel.textContent = `build:${BUILD_STAMP}`;
  stamp.appendChild(stampLabel);
  stamp.id = "build-stamp";

  app.append(heading, subtitle, stamp);
}

render();
