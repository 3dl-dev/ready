// localcachestorage.ts — the ONE place in the shipped bundle that names a
// browser persistence API (ready-fe4).
//
// WHY IT IS ITS OWN FILE, AND WHY IT IS THIS SHORT. nostorage.test.ts scans
// every shipped module and fails if one so much as names localStorage. That
// scan encodes ready-c4b done condition 6 — "No SECRET MATERIAL is ever written
// to localStorage/IndexedDB/sessionStorage" — and it was implemented as a total
// ban because ready-c4b's implementer took the stricter of the two options that
// item permitted ("the item permits [a cache] if it is scoped per logged-in
// pubkey and the decision is written down; the choice made here is the stricter
// one — cache nothing").
//
// ready-fe4 takes the OTHER permitted option, and it does so with the reason
// measured: the portfolio's first paint was 97.2s. Condition 3 of that item
// names the storage explicitly ("localStorage/IndexedDB, NOT sessionStorage")
// and condition 5 names the scoping ("keyed per logged-in pubkey ... discarded
// on logout"). So the ban narrows to a ONE-FILE exemption rather than
// disappearing, and this is that file: it holds the reference and NOTHING else,
// so the scan still covers every line of logic that decides what gets written.
// What is written is decided in boardcache.ts, which names no storage API at
// all and takes the store as a parameter.
//
// THE CONDITION-6 GUARANTEE IS UNCHANGED AND IS NOW ACTUALLY TESTED. No key
// material is cached — boardcache.ts writes CEK EPOCH NUMBERS, never a key and
// never a function of one — and the behavioural guards that assert it
// (main.portfolio.test.ts's "no key material reaches browser storage",
// main.confidential.test.ts's storage sweep) went from covering a page that
// wrote nothing to covering a page that writes a board projection on every
// load.

import type { CacheStorage } from "./boardcache";

/**
 * browserCacheStorage returns the page's localStorage, or undefined where it
 * cannot be reached — Safari's private mode throws on access, an embedding
 * context may have storage blocked, and a non-browser host has none. Undefined
 * means "no cache", which is the page ready-27b shipped, so every caller must
 * treat it as an ordinary outcome rather than an error.
 */
export function browserCacheStorage(): CacheStorage | undefined {
  try {
    const s = globalThis.localStorage;
    return s ? (s as CacheStorage) : undefined;
  } catch {
    return undefined;
  }
}
