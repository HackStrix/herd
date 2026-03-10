import asyncio
from playwright.async_api import async_playwright


async def run_session(session_id: str, url: str, delay: int):
    print(f"[{session_id}] Starting isolated session...")
    
    async with async_playwright() as p:
        # Herd routes based on X-Session-ID header
        print(f"[{session_id}] Connecting to Playwright gateway at ws://127.0.0.1:8080/")
        browser = await p.chromium.connect(
            "ws://127.0.0.1:8080/", 
            headers={"X-Session-ID": session_id}
        )
        
        print(f"[{session_id}] Connected! Opening new context and page.")
        ctx = await browser.new_context()
        page = await ctx.new_page()
        
        print(f"[{session_id}] Navigating to {url}")
        await page.goto(url)
        
        title = await page.title()
        print(f"[{session_id}] Success! Page title is: '{title}'")
        
        print(f"[{session_id}] Simulating user activity and holding connection for {delay} seconds...")
        await asyncio.sleep(delay)
            
        await browser.close()
        print(f"[{session_id}] Session closed gracefully.")

async def main():
    print("=== Testing Herd's Playwright Multi-Tenant Isolation ===")
    print("Spawning two independent sessions. Herd will allocate two separate Playwright workers.")
    
    # Run two isolated sessions concurrently
    await asyncio.gather(
        run_session("session-1-user-A", "https://example.com", 5),
        run_session("session-2-user-B", "https://wikipedia.org", 5)
    )
    async with async_playwright() as p:
        browser = await p.chromium.connect(
            "ws://127.0.0.1:8080/", 
            headers={"X-Session-ID": "session-3-user-C"}
        )

        ctx = await browser.new_context()
        page = await ctx.new_page()

        await page.goto("https://google.com")
        
        title = await page.title()
        print(f"[session-3-user-C] Success! Page title is: '{title}'")

        await asyncio.sleep(20)

        page = await ctx.new_page()
        await page.goto("https://github.com")
        
        title = await page.title()
        print(f"[session-3-user-C] Success! Page title is: '{title}'")

        # delay browser close so as to see ttl effect on cleanup
        await asyncio.sleep(20)
        await browser.close()

    print("=== All tests completed! ===")
    
if __name__ == "__main__":
    asyncio.run(main())
