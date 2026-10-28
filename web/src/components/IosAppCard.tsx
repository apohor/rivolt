import { appButtonLabel, appLinkKind, APP_ROW_HELP, APP_ROW_TITLE } from "../lib/appLink";
import { useIosAppUrl } from "../lib/config";
import { Card } from "./ui";

// Settings → Account: a direct link to the iPhone app, shown only once the server reports one (features.ios_app_url) and never in
// the demo. A plain Apple link: no redirect, no tracking.
export function IosAppCard() {
  const url = useIosAppUrl();
  const kind = appLinkKind(url);
  if (!kind) return null;
  return (
    <Card title={APP_ROW_TITLE} id="iphone-app">
      <p className="text-sm text-neutral-300">{APP_ROW_HELP}</p>
      <a
        href={url}
        target="_blank"
        rel="noopener noreferrer"
        data-ios-app-link
        className="mt-3 inline-block rounded-md border border-neutral-600 bg-neutral-800 px-3 py-1.5 text-sm font-semibold text-neutral-100 hover:border-neutral-500 hover:bg-neutral-700"
      >
        {appButtonLabel(kind)}
      </a>
    </Card>
  );
}
