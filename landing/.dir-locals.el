((nil . ((eval . (progn
                 (setq org-html-validation-link nil
                       org-html-postamble nil
                       org-html-head-include-default-style nil
                       org-html-head-include-scripts nil)
                 (let ((dir (locate-dominating-file default-directory ".dir-locals.el")))
                   (setq org-publish-project-alist
                         `(("pages"
                            :base-directory ,(expand-file-name "org" dir)
                            :base-extension "org"
                            :recursive t
                            :publishing-directory ,(expand-file-name "html" dir)
                            :publishing-function org-html-publish-to-html
                            :html-doctype "html5"
                            :html-html5-fancy t
                            :section-numbers nil
                            :with-title nil
                            :with-author nil
                            :with-date nil
                            :html-postamble nil
                            :auto-sitemap nil)

                           ("static"
                            :base-directory ,(expand-file-name "org" dir)
                            :base-extension "css\\|js\\|txt\\|jpg\\|gif\\|png"
                            :recursive t
                            :publishing-directory ,(expand-file-name "html" dir)
                            :publishing-function org-publish-attachment)

                           ("tracer-landing" :components ("pages" "static"))))))))))
