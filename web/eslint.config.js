// Flat config: JS + TypeScript recommended, Vue 3 recommended (templates parsed with TS).
import js from "@eslint/js";
import vue from "eslint-plugin-vue";
import globals from "globals";
import tseslint from "typescript-eslint";

export default tseslint.config(
  { ignores: ["dist/", "node_modules/", "src/api/schema.d.ts", "src/components.d.ts", "mock/"] },
  js.configs.recommended,
  ...tseslint.configs.recommended,
  ...vue.configs["flat/recommended"],
  {
    files: ["**/*.{ts,tsx,vue}"],
    languageOptions: {
      globals: { ...globals.browser, ...globals.node },
      parserOptions: { parser: tseslint.parser, ecmaFeatures: { jsx: true }, extraFileExtensions: [".vue"] },
    },
    rules: {
      // Building blocks are named after what they are (Page, Panel, Field), like the React app.
      "vue/multi-word-component-names": "off",
      // Prettier-style formatting is not enforced by lint; keep the formatting rules quiet.
      "vue/max-attributes-per-line": "off",
      "vue/singleline-html-element-content-newline": "off",
      "vue/multiline-html-element-content-newline": "off",
      "vue/html-self-closing": "off",
      "vue/html-indent": "off",
      "vue/html-closing-bracket-newline": "off",
      "vue/attributes-order": "off",
      // Optional TS props are undefined when absent; no runtime defaults needed.
      "vue/require-default-prop": "off",
      "@typescript-eslint/no-unused-vars": ["error", { argsIgnorePattern: "^_", varsIgnorePattern: "^_", destructuredArrayIgnorePattern: "^_" }],
    },
  },
  // The office renderer and tests define small render-function components next to each other.
  { files: ["src/office/**/*.tsx", "src/**/*.test.ts"], rules: { "vue/one-component-per-file": "off" } },
);
