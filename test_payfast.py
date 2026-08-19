import asyncio
from playwright.async_api import async_playwright

async def main():
    async with async_playwright() as p:
        browser = await p.chromium.launch(headless=True)
        page = await browser.new_page()
        await page.goto('https://gopayfast.com/docs/', wait_until='networkidle')
        await page.wait_for_timeout(5000) # Wait for CF
        print(await page.content())
        await browser.close()

asyncio.run(main())
