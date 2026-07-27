/// <reference types="vite/client" />

interface ImportMetaEnv {
  readonly VITE_BUILD_STAMP?: string;
}

interface ImportMeta {
  readonly env: ImportMetaEnv;
}
