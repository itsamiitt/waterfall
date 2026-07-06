import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { RouterProvider } from "react-router/dom";
import { AppProviders } from "./app/providers";
import { router } from "./app/router";
import { initTheme } from "./app/theme";
import "./design/tokens.css";
import "./design/base.css";
import "./design/primitives.css";

initTheme(); // set data-theme before first paint (doc 08 §6.1)

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <AppProviders>
      <RouterProvider router={router} />
    </AppProviders>
  </StrictMode>,
);
