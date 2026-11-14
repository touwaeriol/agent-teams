import { GitHubAppPanel } from "@/components/GitHubAppPanel";
import { Loading, Page } from "@/components/common";
import { useGitHubApp } from "@/lib/queries";

/**
 * Deployment step, not part of the product: register the GitHub App that the
 * "连接 GitHub" button installs. Not linked from the navigation; see README.
 */
export function SetupGitHubPage() {
  const app = useGitHubApp();
  return (
    <Page width="narrow" title="部署设置 · GitHub" description="部署后做一次。之后用户只在“Git 连接”页点“连接 GitHub”。">
      {app.data ? <GitHubAppPanel status={app.data} /> : <Loading />}
    </Page>
  );
}
