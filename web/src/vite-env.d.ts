/// <reference types="vite/client" />

interface ImportMetaEnv {
  // CI-stamped build version (the Dockerfile passes VERSION); unset in dev builds.
  readonly VITE_APP_VERSION?: string;
}
