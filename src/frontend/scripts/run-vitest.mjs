import { spawn } from "node:child_process";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = fileURLToPath(new URL(".", import.meta.url));
const vitest = path.resolve(here, "../node_modules/.bin/vitest");
const extra = process.argv
  .slice(2)
  .filter((arg) => !arg.startsWith("--watchAll"));
const child = spawn(vitest, ["run", ...extra], {
  stdio: "inherit",
  shell: process.platform === "win32",
});
child.on("exit", (code) => process.exit(code ?? 1));
