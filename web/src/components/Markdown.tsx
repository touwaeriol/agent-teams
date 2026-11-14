import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import { cn } from "@/lib/utils";

export function Markdown({ children, className }: { children: string; className?: string }) {
  return (
    <div
      className={cn(
        "prose prose-sm prose-invert max-w-none break-words leading-6",
        "prose-p:my-2 prose-ul:my-2 prose-ol:my-2 prose-li:my-0.5 prose-headings:mb-2 prose-headings:mt-4 prose-headings:text-foreground",
        "prose-a:text-primary prose-strong:text-foreground prose-code:rounded prose-code:bg-well prose-code:px-1 prose-code:py-0.5 prose-code:font-mono prose-code:text-[12px] prose-code:before:content-none prose-code:after:content-none",
        "prose-pre:scroll-thin prose-pre:rounded-lg prose-pre:border prose-pre:bg-well prose-pre:text-[12px]",
        "prose-table:text-[13px] prose-th:border-b prose-th:border-border prose-td:border-b prose-td:border-border/60",
        "text-foreground [&>*:first-child]:mt-0 [&>*:last-child]:mb-0",
        className,
      )}
    >
      <ReactMarkdown remarkPlugins={[remarkGfm]}>{children}</ReactMarkdown>
    </div>
  );
}
