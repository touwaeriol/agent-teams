import { FileCode2, FileText, Image, Link2, Palette, Sheet } from "lucide-vue-next";
import type { Component } from "vue";
import type { FileIconKind } from "@/lib/library";

/** The glyph of each library file kind (same icons as the React library). */
export const FILE_ICONS: Record<FileIconKind, Component> = { image: Image, code: FileCode2, text: FileText, table: Sheet, design: Palette, link: Link2 };
