import { render, screen } from "@testing-library/react";
import { expect, it } from "vitest";
import { App } from "./App.tsx";

it("renders the heading", () => {
  render(<App />);
  expect(screen.getByRole("heading", { name: "flywheel" })).toBeInTheDocument();
});
