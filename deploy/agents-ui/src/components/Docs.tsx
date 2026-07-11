import { useEffect, useState } from "react";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import { BookOpen } from "lucide-react";
import { api } from "@/lib/api";
import { Card, CardContent, CardHeader, CardTitle } from "./ui";

/* Renders the embedded user guide (docs/agents.md) as HTML in-app. */
export function Docs() {
  const [md, setMd] = useState<string>("");
  const [err, setErr] = useState("");
  useEffect(() => {
    api
      .docs()
      .then(setMd)
      .catch((e) => setErr(String(e.message)));
  }, []);
  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <BookOpen className="h-4 w-4 text-primary" /> Local Agents — user guide
        </CardTitle>
      </CardHeader>
      <CardContent>
        {err && <p className="text-sm text-destructive">Failed to load docs: {err}</p>}
        {!err && !md && <p className="text-sm text-muted-foreground">Loading…</p>}
        <article
          className="prose prose-sm max-w-none dark:prose-invert
            prose-headings:scroll-mt-20 prose-headings:font-semibold
            prose-a:text-primary prose-code:rounded prose-code:bg-muted prose-code:px-1
            prose-code:py-0.5 prose-code:font-mono prose-code:text-xs prose-code:before:content-none
            prose-code:after:content-none prose-pre:bg-popover prose-pre:border
            prose-th:text-left prose-img:rounded-md"
        >
          <ReactMarkdown remarkPlugins={[remarkGfm]}>{md}</ReactMarkdown>
        </article>
      </CardContent>
    </Card>
  );
}
