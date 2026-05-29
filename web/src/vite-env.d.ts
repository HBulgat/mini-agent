/// <reference types="vite/client" />

// Tell TS about the asset imports Vite handles at build time. Without
// these declarations, importing a `.css` file (or future `.svg`/`.png`)
// trips `TS2307: Cannot find module`.
declare module '*.css';
declare module '*.svg';
declare module '*.png';
declare module '*.jpg';
declare module '*.jpeg';
declare module '*.webp';
