import type { Metadata, Viewport } from "next"
import localFont from "next/font/local"

import { Providers } from "./providers"
import "./globals.css"

const inter = localFont({ src: "./fonts/Inter.ttf", variable: "--font-inter", weight: "100 900", display: "swap" })
const archivo = localFont({ src: "./fonts/Archivo.ttf", variable: "--font-archivo", weight: "100 900", display: "swap" })
const jetbrains = localFont({ src: "./fonts/JetBrainsMono.ttf", variable: "--font-jetbrains", weight: "100 800", display: "swap" })

export const metadata: Metadata = {
  title: { default: "Aether", template: "%s · Aether" },
  description: "Aether is a self-hosted email platform for organizations: mail, domains, mailboxes and single sign-on in one place.",
  applicationName: "Aether",
  icons: {
    icon: [
      { url: "/favicon.ico", sizes: "any" },
      { url: "/favicon.svg", type: "image/svg+xml" },
      { url: "/icon-192.png", sizes: "192x192", type: "image/png" },
      { url: "/icon-512.png", sizes: "512x512", type: "image/png" },
    ],
    apple: "/apple-touch-icon.png",
  },
}

export const viewport: Viewport = {
  themeColor: "#111111",
}

export default function RootLayout({ children }: LayoutProps<"/">) {
  return (
    <html lang="en" suppressHydrationWarning className={`${inter.variable} ${archivo.variable} ${jetbrains.variable} h-full antialiased`}>
      <body className="h-full">
        <Providers>{children}</Providers>
      </body>
    </html>
  )
}
