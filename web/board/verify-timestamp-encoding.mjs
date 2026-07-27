// verify-timestamp-encoding.mjs — ready-414 review finding #6.
//
// The vector file's claim (spec §4.8, docs/design/board-fold-spec.md) is that
// an independent TypeScript/JavaScript client can JSON.parse
// testdata/fold.vectors.json and recover expect.items[].created_at /
// .updated_at EXACTLY via BigInt(), never Number(). Every test proving that
// so far (internal/foldvectors/vectors_test.go) is Go-side: it never actually
// runs a JS engine over the committed file. Go's float64 and JS's Number are
// both IEEE-754 doubles, so the precision reasoning transfers — but this
// script removes the "transfers" step and does the real thing: a real
// node process, JSON.parse, BigInt, over the actual committed file.
//
// Usage: node verify-timestamp-encoding.mjs <path-to-fold.vectors.json>
// Exits non-zero and prints a diagnostic on any failure; never skips.

import { readFileSync } from "node:fs";

const path = process.argv[2];
if (!path) {
	console.error("usage: node verify-timestamp-encoding.mjs <path-to-fold.vectors.json>");
	process.exit(1);
}

const doc = JSON.parse(readFileSync(path, "utf8"));
if (!Array.isArray(doc.vectors) || doc.vectors.length === 0) {
	console.error(`FAIL: ${path} has no vectors — looks truncated or malformed`);
	process.exit(1);
}

const digitString = /^-?[0-9]+$/;
let checked = 0;
for (const v of doc.vectors) {
	for (const item of v.expect.items) {
		for (const field of ["created_at", "updated_at"]) {
			const raw = item[field];
			if (typeof raw !== "string" || !digitString.test(raw)) {
				console.error(
					`FAIL: vector ${v.name} item ${item.id ?? "?"} field ${field} = ${JSON.stringify(raw)} ` +
						`is not a decimal-digit JSON string`,
				);
				process.exit(1);
			}
			// BigInt() throws on anything it cannot parse exactly as an integer
			// literal; a round-trip through toString() must reproduce the
			// original digits, or precision was lost somewhere in this chain.
			const big = BigInt(raw);
			if (big.toString() !== raw) {
				console.error(
					`FAIL: vector ${v.name} item ${item.id ?? "?"} field ${field}: BigInt round-trip ` +
						`mismatch (${raw} -> ${big.toString()})`,
				);
				process.exit(1);
			}
			checked++;
		}
	}
}

// The specific counterexample the spec §4.8 fix depends on: a value the live
// fold actually produced that a bare Number() parse would have corrupted.
// This proves the defect is real IN THIS JS ENGINE, not only reasoned about
// by analogy to Go's float64.
const target = doc.vectors.find((v) => v.name === "item_timestamp_above_float64_safe_bound");
if (!target) {
	console.error("FAIL: required vector item_timestamp_above_float64_safe_bound is missing");
	process.exit(1);
}
const created = target.expect.items[0].created_at;
const exact = BigInt(created);
const lossyViaNumber = BigInt(Math.trunc(Number(created)));
if (exact === lossyViaNumber) {
	console.error(
		`FAIL: expected item_timestamp_above_float64_safe_bound's created_at (${created}) to be a ` +
			`genuine Number() counterexample, but Number()-then-BigInt matched exactly`,
	);
	process.exit(1);
}

console.log(
	`OK: node verified ${checked} created_at/updated_at fields across ${doc.vectors.length} vectors; ` +
		`confirmed a bare Number() parse of ${created} would have produced ${lossyViaNumber.toString()} ` +
		`(BigInt-exact value is what expect.items actually carries)`,
);
