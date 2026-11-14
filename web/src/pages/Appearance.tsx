import { Moon, Sun } from "lucide-react";
import { Page, Panel } from "@/components/common";
import { useTheme, type Theme } from "@/lib/theme";
import { cn } from "@/lib/utils";

const OPTIONS: { value: Theme; label: string; icon: typeof Sun }[] = [
  { value: "light", label: "亮色", icon: Sun },
  { value: "dark", label: "暗色", icon: Moon },
];

export function AppearancePage() {
  const { theme, setTheme } = useTheme();
  return (
    <Page width="narrow" title="外观" description="只影响这台设备上的浏览器。">
      <Panel>
        <div className="grid grid-cols-2 gap-3">
          {OPTIONS.map((o) => (
            <button
              key={o.value}
              type="button"
              onClick={() => setTheme(o.value)}
              aria-pressed={theme === o.value}
              className={cn("flex items-center gap-3 rounded-xl border p-4 text-left hover:bg-raised", theme === o.value && "border-foreground/40 bg-raised")}
            >
              <o.icon className="size-5 text-muted-foreground" />
              <span className="font-medium">{o.label}</span>
            </button>
          ))}
        </div>
      </Panel>
    </Page>
  );
}
