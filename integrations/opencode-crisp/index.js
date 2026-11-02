// Crisp quality gate plugin for opencode.
//
// IMPORTANT (verified against the opencode 1.18.18 loader):
// opencode iterates every value of this module's namespace and throws
// "Plugin export is not a function" for any value that is neither a function
// nor an object with a callable 'server' field. Therefore this module must
// keep exactly ONE export: the default async plugin factory.
//
// opencode calls this factory once at server start with
//   input  = { client, project, worktree, directory, serverUrl, $, ... }
//   options = the second element of a ["spec", { ... }] plugin tuple
// and expects a hooks object (see ./lib/hooks.js for the "event" hook).
import { createCrispHooks } from "./lib/hooks.js";

export default async function crispOpencodePlugin(input, options) {
  return createCrispHooks(input, options ?? {});
}
