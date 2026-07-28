import { RELAYS } from "./dep";

// RELAYS is rendered, not merely imported, so the bundler cannot tree-shake
// the literals this fixture exists to carry into the built chunk.
const el = document.getElementById("app");
if (el) el.textContent = RELAYS.join(",");
