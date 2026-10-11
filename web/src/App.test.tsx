import { render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { App } from "./App.tsx";
import { installFakes } from "./testing/fakes.ts";

afterEach(() => vi.unstubAllGlobals());

it("renders the heading", () => {
  installFakes();
  render(<App />);
  expect(screen.getByRole("heading", { name: "flywheel" })).toBeInTheDocument();
});
