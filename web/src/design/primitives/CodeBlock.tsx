import { useState } from "react";
import { Button } from "./Button";

export interface CodeBlockProps {
  code: string;
  language?: "json";
  /** Copies ids / envelope ids — never secrets (none exist client-side; doc 08 §6.2). */
  copyable?: boolean;
}

export function CodeBlock({ code, copyable = false }: CodeBlockProps) {
  const [copied, setCopied] = useState(false);

  async function copy() {
    try {
      await navigator.clipboard.writeText(code);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      /* clipboard denied: the text remains selectable */
    }
  }

  return (
    <div className="p-code">
      {copyable ? (
        <span className="p-code-copy">
          <Button size="sm" variant="ghost" onClick={copy}>
            {copied ? "Copied" : "Copy"}
          </Button>
        </span>
      ) : null}
      <pre>
        <code>{code}</code>
      </pre>
    </div>
  );
}
