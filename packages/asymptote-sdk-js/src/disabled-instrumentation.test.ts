import * as Anthropic from "@anthropic-ai/sdk";
import { afterEach, expect, it } from "vitest";
import { initialize, patch, shutdown } from "./index.js";

const resources = [Anthropic.Anthropic.Completions, Anthropic.Anthropic.Messages, Anthropic.Anthropic.Beta.Messages];
const originals = resources.map(resource => resource.prototype.create);
const descriptors = resources.map(resource => Object.getOwnPropertyDescriptor(resource.prototype, "create")!);

afterEach(async () => {
  await shutdown();
  // Manual upstream patching mutates provider prototypes. Restore the fixture's
  // real SDK classes explicitly so a failing regression cannot affect controls.
  resources.forEach((resource, index) => { Object.defineProperty(resource.prototype, "create", descriptors[index]); });
});

it.each(["initial modules", "later patch"])("keeps disabled providers unpatched with %s", (mode) => {
  initialize({
    disableDefaultExporter: true,
    instrumentationOptions: { openAI: false, anthropic: false },
    ...(mode === "initial modules" ? { instrumentModules: { Anthropic } } : {}),
  });
  if (mode === "later patch") patch({ Anthropic });
  resources.forEach((resource, index) => { expect(resource.prototype.create).toBe(originals[index]); });
});

it("still manually patches the real provider when it is enabled", () => {
  initialize({ disableDefaultExporter: true, instrumentationOptions: { openAI: false }, instrumentModules: { Anthropic } });
  expect(Anthropic.Anthropic.Messages.prototype.create).not.toBe(originals[1]);
});

it("keeps globally disabled instrumentations unpatched", () => {
  initialize({ disableDefaultExporter: true, disableInstrumentations: true, instrumentModules: { Anthropic } });
  resources.forEach((resource, index) => { expect(resource.prototype.create).toBe(originals[index]); });
});
