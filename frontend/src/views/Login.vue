<template>
  <div id="login" :class="{ recaptcha: recaptcha }">
    <form @submit="submit">
      <img :src="logoURL" alt="File Browser" />
      <h1>{{ name }}</h1>
      <p v-if="reason != null" class="logout-message">
        {{
          count === null
            ? t(`login.logout_reasons.${reason}`)
            : t(`login.logout_reasons.${reason}`, { count }, count)
        }}
      </p>
      <div v-if="error !== ''" class="wrong">{{ error }}</div>

      <input
        autofocus
        class="input input--block"
        type="text"
        autocapitalize="off"
        v-model="username"
        :placeholder="t('login.username')"
      />
      <input
        class="input input--block"
        type="password"
        v-model="password"
        :placeholder="t('login.password')"
      />
      <input
        class="input input--block"
        v-if="createMode"
        type="password"
        v-model="passwordConfirm"
        :placeholder="t('login.passwordConfirm')"
      />

      <div v-if="recaptcha" id="recaptcha"></div>
      <input
        class="button button--block"
        type="submit"
        :value="createMode ? t('login.signup') : t('login.submit')"
      />

      <p @click="toggleMode" v-if="signup">
        {{ createMode ? t("login.loginInstead") : t("login.createAnAccount") }}
      </p>
    </form>
  </div>
</template>

<script setup lang="ts">
import { StatusError } from "@/api/utils";
import { tooManyAttemptsMessage } from "@/utils/errors";
import * as auth from "@/utils/auth";
import {
  name,
  logoURL,
  recaptcha,
  recaptchaKey,
  signup,
} from "@/utils/constants";
import { inject, onMounted, ref } from "vue";
import { useI18n } from "vue-i18n";
import { useRoute, useRouter } from "vue-router";

// Define refs
const createMode = ref<boolean>(false);
const error = ref<string>("");
const username = ref<string>("");
const password = ref<string>("");
const passwordConfirm = ref<string>("");

const route = useRoute();
const router = useRouter();
const { t } = useI18n({});
// Define functions
const toggleMode = () => (createMode.value = !createMode.value);

const $showError = inject<IToastError>("$showError")!;

// uploadsCancelled comes with the number of uploads (cancelled-uploads), a
// positive whole number, or it is not shown
const cancelled = Number(route.query["cancelled-uploads"] || NaN);
const count = Number.isInteger(cancelled) && cancelled > 0 ? cancelled : null;
const rawReason = route.query["logout-reason"] ?? null;
const reason =
  rawReason === "uploadsCancelled" && count === null ? null : rawReason;

// Where to go once logged in: the page given as redirect (the one a forced
// logout left), query included, if it is a path of this app; else the
// default page. A path with an empty segment once decoded ("//host/x",
// "/%2Fx", or "/\host", which a browser reads as "//host") is not one: no
// route matches it, and the catch-all route redirected it to itself until
// router.push failed.
const redirectTarget = (): string => {
  const value = route.query.redirect;
  if (typeof value !== "string" || !value.startsWith("/")) return "/files/";
  let path = value.split(/[?#]/)[0];
  try {
    path = decodeURIComponent(path);
  } catch {
    return "/files/";
  }
  return path.replace(/\\/g, "/").includes("//") ? "/files/" : value;
};

const submit = async (event: Event) => {
  event.preventDefault();
  event.stopPropagation();

  const redirect = redirectTarget();

  let captcha = "";
  if (recaptcha) {
    captcha = window.grecaptcha.getResponse();

    if (captcha === "") {
      error.value = t("login.wrongCredentials");
      return;
    }
  }

  if (createMode.value) {
    if (password.value !== passwordConfirm.value) {
      error.value = t("login.passwordsDontMatch");
      return;
    }
  }

  try {
    if (createMode.value) {
      await auth.signup(username.value, password.value);
    }

    await auth.login(username.value, password.value, captcha);
    // anything the check above lets through that the router cannot go to
    router.push(redirect).catch(() => router.push("/files/"));
  } catch (e: any) {
    // console.error(e);
    if (e instanceof StatusError) {
      if (e.status === 409) {
        error.value = t("login.usernameTaken");
      } else if (e.status === 403) {
        error.value = t("login.wrongCredentials");
      } else if (e.status === 429) {
        error.value = tooManyAttemptsMessage(t, e.retryAfter);
      } else if (e.status === 400) {
        const match = e.message.match(/minimum length is (\d+)/);
        if (match) {
          error.value = t("login.passwordTooShort", { min: match[1] });
        } else {
          error.value = e.message;
        }
      } else {
        $showError(e);
      }
    }
  }
};

// Run hooks
onMounted(() => {
  if (!recaptcha) return;

  window.grecaptcha.ready(function () {
    window.grecaptcha.render("recaptcha", {
      sitekey: recaptchaKey,
    });
  });
});
</script>
