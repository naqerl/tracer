function resize(iframe) {
    var doc;
    try {
        doc = iframe.contentDocument || iframe.contentWindow.document;
    } catch(e) {}
    if (!doc) return;
    iframe.style.height = doc.documentElement.scrollHeight + 'px';
}

function observe(iframe) {
    console.log("observing", iframe)
    iframe.addEventListener('load', () => {
        const iframeDoc = iframe.contentDocument || iframe.contentWindow.document;

        // Configure MutationObserver to watch for DOM changes
        const observer = new MutationObserver(() => {
            const contentHeight = iframeDoc.body.scrollHeight;
            iframe.style.height = `${contentHeight + 20}px`;
        });

        // Observe body and its children for changes
        observer.observe(iframeDoc.body, {
            childList: true, // New/deleted elements
            subtree: true,   // Deep changes (children of children)
            attributes: true, // Attribute changes (e.g., style)
            characterData: true // Text content changes
        });

        // Initial height set
        iframe.style.height = `${iframeDoc.body.scrollHeight + 20}px`;
    });
}

document.querySelectorAll('iframe').forEach(observe)
