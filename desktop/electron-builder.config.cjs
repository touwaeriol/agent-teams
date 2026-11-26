// electron-builder configuration. Signing and notarization switch on by themselves when the
// environment has the credentials (README.md 签名和公证); without them the build is unsigned
// and still succeeds.
//
//   Mac signing:      CSC_LINK (+ CSC_KEY_PASSWORD)            Developer ID Application .p12
//   Mac notarization: APPLE_ID, APPLE_APP_SPECIFIC_PASSWORD, APPLE_TEAM_ID
//   Windows (.pfx):   WIN_CSC_LINK (+ WIN_CSC_KEY_PASSWORD)     or CSC_LINK on a Windows runner
//   Windows (Azure Artifact Signing): AZURE_SIGN_ENDPOINT, AZURE_SIGN_ACCOUNT, AZURE_SIGN_PROFILE
//                                     + AZURE_TENANT_ID, AZURE_CLIENT_ID, AZURE_CLIENT_SECRET
const env = process.env;
const macSigned = Boolean(env.CSC_LINK) || env.CSC_IDENTITY_AUTO_DISCOVERY === "true";
const azure = env.AZURE_SIGN_ENDPOINT && env.AZURE_SIGN_ACCOUNT && env.AZURE_SIGN_PROFILE;

/** @type {import("electron-builder").Configuration} */
module.exports = {
  appId: "ai.prophey.agentteams",
  productName: "Agent Teams",
  copyright: "Copyright © touwaeriol",
  directories: { output: "release", buildResources: "build" },
  files: ["dist/**/*", "package.json", "!**/*.map"],
  asar: true,
  protocols: [{ name: "Agent Teams", schemes: ["agentteams"] }],
  // Releases are tagged desktop-v<version> (the repo's other tags stay out of the way). The CI
  // workflow builds with --publish never and uploads the files itself (ci/desktop-release.yml);
  // this entry still goes into app-update.yml, which electron-updater reads.
  publish: [{ provider: "github", owner: "touwaeriol", repo: "agent-teams", releaseType: "release", tagNamePrefix: "desktop-v" }],
  artifactName: "${productName}-${version}-${os}-${arch}.${ext}",

  // Flipped in the Electron binary at package time: the shipped app cannot be turned into a
  // plain Node runtime (ELECTRON_RUN_AS_NODE, NODE_OPTIONS, --inspect) or load code outside app.asar.
  electronFuses: {
    runAsNode: false,
    enableNodeOptionsEnvironmentVariable: false,
    enableNodeCliInspectArguments: false,
    onlyLoadAppFromAsar: true,
    enableCookieEncryption: true,
  },

  mac: {
    category: "public.app-category.productivity",
    target: [
      { target: "dmg", arch: ["x64", "arm64"] },
      { target: "zip", arch: ["x64", "arm64"] }, // zip is what electron-updater installs on Mac
    ],
    // Signed: Developer ID + hardened runtime (notarization needs it). Unsigned: ad-hoc
    // signature so the arm64 build starts at all; hardened runtime would need
    // disable-library-validation for an ad-hoc signature, so it is off then.
    identity: macSigned ? undefined : "-",
    hardenedRuntime: macSigned,
    gatekeeperAssess: false,
    entitlements: "build/entitlements.mac.plist",
    entitlementsInherit: "build/entitlements.mac.plist",
    extendInfo: {
      NSMicrophoneUsageDescription: "Agent Teams 需要使用麦克风来进行语音输入。",
    },
  },
  dmg: { artifactName: "${productName}-${version}-mac-${arch}.${ext}" },

  win: {
    // x64 only; add "arm64" for Windows on ARM (one installer then carries both and is twice the size).
    target: [{ target: "nsis", arch: ["x64"] }],
    ...(azure
      ? {
          azureSignOptions: {
            endpoint: env.AZURE_SIGN_ENDPOINT,
            codeSigningAccountName: env.AZURE_SIGN_ACCOUNT,
            certificateProfileName: env.AZURE_SIGN_PROFILE,
          },
        }
      : {}),
  },
  nsis: {
    oneClick: false,
    perMachine: false,
    allowToChangeInstallationDirectory: true,
    createDesktopShortcut: true,
    artifactName: "${productName}-Setup-${version}-${arch}.${ext}",
  },

  // Not shipped; lets the packaging config be checked on a Linux machine (`--linux dir`).
  linux: { target: ["dir"], category: "Network" },
};
